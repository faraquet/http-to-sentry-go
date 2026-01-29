package fastly

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

type Event struct {
	Timestamp        string `json:"timestamp"`
	ClientIP         string `json:"client_ip"`
	GeoCountry       string `json:"geo_country"`
	GeoCity          string `json:"geo_city"`
	Host             string `json:"host"`
	URL              string `json:"url"`
	OriginalURL      string `json:"original_url"`
	RequestMethod    string `json:"request_method"`
	RequestProtocol  string `json:"request_protocol"`
	RequestReferer   string `json:"request_referer"`
	RequestUserAgent string `json:"request_user_agent"`
	ResponseState    string `json:"response_state"`
	ResponseStatus   int    `json:"response_status"`
	ResponseReason   string `json:"response_reason"`
	ResponseBodySize int64  `json:"response_body_size"`
	TLSClientJA3MD5  string `json:"tls_client_ja3_md5"`
	FastlyServer     string `json:"fastly_server"`
	FastlyIsEdge     bool   `json:"fastly_is_edge"`
}

type Handler struct {
	MaxBodyBytes int
	Capture      func(*sentry.Event) *sentry.EventID
}

func (h Handler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	maxBytes := h.MaxBodyBytes
	if maxBytes <= 0 {
		maxBytes = 262144
	}

	body, tooLarge, err := readLimitedBody(r.Body, maxBytes)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if tooLarge {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	events, ok := parseEvents(body)
	if !ok || len(events) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	capture := h.Capture
	if capture == nil {
		capture = sentry.CaptureEvent
	}

	eventIDs := make([]string, 0, len(events))
	for _, fe := range events {
		event := buildSentryEvent(fe, r)
		eventID := capture(event)
		if eventID == nil {
			continue
		}
		if id := string(*eventID); id != "" {
			eventIDs = append(eventIDs, id)
		}
	}

	resp, err := json.Marshal(map[string]interface{}{
		"event_ids": eventIDs,
	})
	if err != nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(resp)
}

func ChallengeHandler(serviceID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		serviceID = strings.TrimSpace(serviceID)
		if serviceID == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sum := sha256.Sum256([]byte(serviceID))
		value := hex.EncodeToString(sum[:])
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(value + "\n"))
	}
}

func buildSentryEvent(fe Event, r *http.Request) *sentry.Event {
	event := sentry.NewEvent()
	event.Logger = "fastly"
	event.Timestamp = time.Now()
	event.Level = mapLevel(fe)

	message := buildMessage(fe)
	if message == "" {
		message = "fastly event"
	}
	event.Message = message

	event.Tags = map[string]string{
		"host":             fe.Host,
		"response_state":   fe.ResponseState,
		"request_method":   fe.RequestMethod,
		"request_protocol": fe.RequestProtocol,
		"fastly_server":    fe.FastlyServer,
	}
	addTag(event.Tags, "geo_country", fe.GeoCountry)
	addTag(event.Tags, "geo_city", fe.GeoCity)
	addTag(event.Tags, "tls_client_ja3_md5", fe.TLSClientJA3MD5)
	if fe.FastlyIsEdge {
		event.Tags["fastly_is_edge"] = "true"
	}

	event.Extra = map[string]interface{}{
		"fastly":           fe,
		"fastly_timestamp": fe.Timestamp,
	}

	reqURL := buildURL(fe)
	if reqURL != "" {
		event.Request = &sentry.Request{
			URL:         reqURL,
			Method:      fe.RequestMethod,
			Headers:     map[string]string{"User-Agent": fe.RequestUserAgent, "Referer": fe.RequestReferer},
			QueryString: queryStringFromURL(reqURL),
		}
	}

	if fe.ClientIP != "" {
		event.User = sentry.User{IPAddress: fe.ClientIP}
	}

	addTag(event.Tags, "remote_addr", r.RemoteAddr)
	return event
}

func buildMessage(fe Event) string {
	state := titleCaseSimple(strings.TrimSpace(fe.ResponseState))
	status := ""
	if fe.ResponseStatus != 0 {
		status = strconv.Itoa(fe.ResponseStatus)
	}
	reason := strings.TrimSpace(fe.ResponseReason)

	message := strings.TrimSpace("HTTP::" + strings.Join([]string{state, status}, ""))
	if message == "HTTP::" {
		message = "HTTP"
	}
	if reason == "" {
		return message
	}
	return message + " (" + reason + ")"
}

func mapLevel(fe Event) sentry.Level {
	state := strings.ToLower(strings.TrimSpace(fe.ResponseState))
	switch state {
	case "error", "fail", "failed":
		return sentry.LevelError
	case "warning", "warn":
		return sentry.LevelWarning
	}

	if fe.ResponseStatus >= 500 {
		return sentry.LevelError
	}
	if fe.ResponseStatus >= 400 {
		return sentry.LevelWarning
	}
	return sentry.LevelInfo
}

func buildURL(fe Event) string {
	if fe.Host == "" && fe.URL == "" {
		return ""
	}
	path := fe.URL
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return "https://" + fe.Host + path
}

func queryStringFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.RawQuery
}

func parseEvents(body []byte) ([]Event, bool) {
	var single Event
	if err := json.Unmarshal(body, &single); err == nil {
		return []Event{single}, true
	}

	var multiple []Event
	if err := json.Unmarshal(body, &multiple); err == nil {
		return multiple, true
	}

	return nil, false
}

func readLimitedBody(body io.ReadCloser, maxBytes int) ([]byte, bool, error) {
	defer body.Close()
	limit := int64(maxBytes)
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

func addTag(tags map[string]string, key, value string) {
	if value == "" {
		return
	}
	tags[key] = value
}

func titleCaseSimple(value string) string {
	if value == "" {
		return ""
	}
	value = strings.ToLower(value)
	return strings.ToUpper(value[:1]) + value[1:]
}
