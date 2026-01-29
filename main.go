package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
)

type config struct {
	httpAddr      string
	httpPath      string
	fastlyPath    string
	maxBodyBytes  int
	flushTimeout  time.Duration
	shutdownGrace time.Duration
}

type payload struct {
	Message   string                 `json:"message"`
	Level     string                 `json:"level"`
	Timestamp string                 `json:"timestamp"`
	Tags      map[string]string      `json:"tags"`
	Extra     map[string]interface{} `json:"extra"`
}

type fastlyEvent struct {
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

func main() {
	cfg := loadConfig()

	if err := initSentry(); err != nil {
		log.Fatalf("sentry init: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.httpPath, func(w http.ResponseWriter, r *http.Request) {
		handleIngest(w, r, cfg)
	})
	mux.HandleFunc(cfg.fastlyPath, func(w http.ResponseWriter, r *http.Request) {
		handleFastly(w, r, cfg)
	})

	srv := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("ready: http=%s ingest=%s fastly=%s", cfg.httpAddr, cfg.httpPath, cfg.fastlyPath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("http server error: %v", err)
	}

	sentry.Flush(cfg.flushTimeout)
}

func loadConfig() config {
	maxBodyBytes := envInt("HTTP_MAX_BODY_BYTES", 262144)
	if maxBodyBytes < 1024 {
		maxBodyBytes = 1024
	}

	httpAddr := envOrDefault("HTTP_ADDR", "0.0.0.0:8080")
	httpPath := envOrDefault("HTTP_PATH", "/ingest")
	fastlyPath := envOrDefault("HTTP_FASTLY_PATH", "/fastly")
	if !strings.HasPrefix(httpPath, "/") {
		httpPath = "/" + httpPath
	}
	if !strings.HasPrefix(fastlyPath, "/") {
		fastlyPath = "/" + fastlyPath
	}

	flushTimeout := time.Duration(envInt("SENTRY_FLUSH_TIMEOUT_MS", 2000)) * time.Millisecond
	if flushTimeout <= 0 {
		flushTimeout = 2 * time.Second
	}

	shutdownGrace := time.Duration(envInt("HTTP_SHUTDOWN_TIMEOUT_MS", 5000)) * time.Millisecond
	if shutdownGrace <= 0 {
		shutdownGrace = 5 * time.Second
	}

	return config{
		httpAddr:      httpAddr,
		httpPath:      httpPath,
		fastlyPath:    fastlyPath,
		maxBodyBytes:  maxBodyBytes,
		flushTimeout:  flushTimeout,
		shutdownGrace: shutdownGrace,
	}
}

func initSentry() error {
	env := strings.TrimSpace(os.Getenv("SENTRY_ENVIRONMENT"))
	if env == "" {
		env = "development"
	}

	options := sentry.ClientOptions{
		Dsn:         strings.TrimSpace(os.Getenv("SENTRY_DSN")),
		Environment: env,
		Release:     strings.TrimSpace(os.Getenv("SENTRY_RELEASE")),
	}

	if options.Dsn == "" {
		log.Printf("SENTRY_DSN is empty; events will be dropped")
	}

	return sentry.Init(options)
}

func handleIngest(w http.ResponseWriter, r *http.Request, cfg config) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, tooLarge, err := readLimitedBody(r.Body, cfg.maxBodyBytes)
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

	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	parsedPayload, parsed := parsePayload(contentType, body)

	event := sentry.NewEvent()
	event.Logger = "http"
	event.Level = sentry.LevelInfo
	event.Timestamp = time.Now()

	event.Tags = map[string]string{
		"remote_addr": r.RemoteAddr,
		"method":      r.Method,
		"path":        r.URL.Path,
	}

	if parsed {
		event.Message = parsedPayload.Message
		if event.Message == "" {
			event.Message = string(body)
		}
		event.Level = parseLevel(parsedPayload.Level)
		if parsedPayload.Tags != nil {
			for key, value := range parsedPayload.Tags {
				if key != "" && value != "" {
					event.Tags[key] = value
				}
			}
		}
		if parsedPayload.Extra != nil {
			event.Extra = parsedPayload.Extra
		}
		if parsedPayload.Timestamp != "" {
			if event.Extra == nil {
				event.Extra = map[string]interface{}{}
			}
			event.Extra["payload_timestamp"] = parsedPayload.Timestamp
		}
	} else {
		event.Message = string(body)
		event.Extra = map[string]interface{}{
			"raw": string(body),
		}
	}

	if event.Message == "" {
		event.Message = "(empty message)"
	}

	eventID := sentry.CaptureEvent(event)
	if eventID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	eventIDStr := string(*eventID)
	if eventIDStr == "" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"event_id\":\"" + eventIDStr + "\"}"))
}

func handleFastly(w http.ResponseWriter, r *http.Request, cfg config) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, tooLarge, err := readLimitedBody(r.Body, cfg.maxBodyBytes)
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

	events, ok := parseFastlyEvents(body)
	if !ok || len(events) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	eventIDs := make([]string, 0, len(events))
	for _, fe := range events {
		event := buildFastlySentryEvent(fe, r)
		eventID := sentry.CaptureEvent(event)
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

func buildFastlySentryEvent(fe fastlyEvent, r *http.Request) *sentry.Event {
	event := sentry.NewEvent()
	event.Logger = "fastly"
	event.Timestamp = time.Now()
	event.Level = mapFastlyLevel(fe)

	message := buildFastlyMessage(fe)
	if message == "" {
		message = "fastly event"
	}
	event.Message = message

	if ts, err := parseFastlyTimestamp(fe.Timestamp); err == nil {
		event.Timestamp = ts
	}

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

	reqURL := buildFastlyURL(fe)
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

func buildFastlyMessage(fe fastlyEvent) string {
	state := strings.ToUpper(strings.TrimSpace(fe.ResponseState))
	status := ""
	if fe.ResponseStatus != 0 {
		status = strconv.Itoa(fe.ResponseStatus)
	}
	message := strings.TrimSpace("FASTLY " + strings.TrimSpace(strings.Join([]string{state, status}, " ")))
	if message == "FASTLY" {
		return "FASTLY"
	}
	return message
}

func mapFastlyLevel(fe fastlyEvent) sentry.Level {
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

func buildFastlyURL(fe fastlyEvent) string {
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

func parsePayload(contentType string, body []byte) (payload, bool) {
	if !strings.Contains(contentType, "application/json") {
		return payload{}, false
	}

	var parsed payload
	if err := json.Unmarshal(body, &parsed); err != nil {
		return payload{}, false
	}

	return parsed, true
}

func parseFastlyEvents(body []byte) ([]fastlyEvent, bool) {
	var single fastlyEvent
	if err := json.Unmarshal(body, &single); err == nil {
		return []fastlyEvent{single}, true
	}

	var multiple []fastlyEvent
	if err := json.Unmarshal(body, &multiple); err == nil {
		return multiple, true
	}

	return nil, false
}

func parseLevel(level string) sentry.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "fatal":
		return sentry.LevelFatal
	case "error":
		return sentry.LevelError
	case "warning", "warn":
		return sentry.LevelWarning
	case "debug":
		return sentry.LevelDebug
	case "info", "":
		return sentry.LevelInfo
	default:
		return sentry.LevelInfo
	}
}

func addTag(tags map[string]string, key, value string) {
	if value == "" {
		return
	}
	tags[key] = value
}

func envOrDefault(key, def string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return def
	}
	return value
}

func envInt(key string, def int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return def
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return def
	}
	return parsed
}
