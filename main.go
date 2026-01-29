package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	"http-to-sentry-go/fastly"
)

type config struct {
	httpAddr        string
	httpsAddr       string
	httpsCertFile   string
	httpsKeyFile    string
	httpPath        string
	fastlyPath      string
	fastlyServiceID string
	authToken       string
	maxBodyBytes    int
	flushTimeout    time.Duration
	shutdownGrace   time.Duration
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
		if !requireBearer(w, r, cfg) {
			return
		}
		handleIngest(w, r, cfg)
	})
	fastlyHandler := fastly.Handler{
		MaxBodyBytes: cfg.maxBodyBytes,
		Capture:      sentry.CaptureEvent,
	}
	mux.HandleFunc(cfg.fastlyPath, func(w http.ResponseWriter, r *http.Request) {
		if !requireBearer(w, r, cfg) {
			return
		}
		fastlyHandler.HandleEvents(w, r)
	})
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/.well-known/fastly/logging/challenge", fastly.ChallengeHandler(cfg.fastlyServiceID))

	handler := loggingMiddleware(mux, 4096)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	if cfg.httpAddr != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runHTTP(ctx, cfg.httpAddr, handler, cfg.shutdownGrace); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("http server error: %v", err)
			}
		}()
	}
	if cfg.httpsAddr != "" && cfg.httpsCertFile != "" && cfg.httpsKeyFile != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runHTTPS(ctx, cfg.httpsAddr, cfg.httpsCertFile, cfg.httpsKeyFile, handler, cfg.shutdownGrace); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("https server error: %v", err)
			}
		}()
	}

	log.Printf("ready: http=%s https=%s ingest=%s fastly=%s", cfg.httpAddr, cfg.httpsAddr, cfg.httpPath, cfg.fastlyPath)
	<-ctx.Done()
	log.Printf("shutting down")

	wg.Wait()
	sentry.Flush(cfg.flushTimeout)
}

func requireBearer(w http.ResponseWriter, r *http.Request, cfg config) bool {
	if cfg.authToken == "" {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth == "Bearer "+cfg.authToken {
		return true
	}
	w.WriteHeader(http.StatusUnauthorized)
	return false
}

func loggingMiddleware(next http.Handler, maxLogBytes int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		var payload string
		if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			limit := int64(maxLogBytes)
			data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
			if err == nil {
				truncated := int64(len(data)) > limit
				if truncated {
					data = data[:limit]
				}
				payload = string(data)
				if truncated {
					payload += "…"
				}
				// Restore body for downstream handlers.
				r.Body = io.NopCloser(bytes.NewReader(data))
			}
		}

		next.ServeHTTP(ww, r)
		if payload != "" {
			log.Printf("%s %s %d %s %s payload=%q", r.Method, r.URL.Path, ww.status, time.Since(start).Truncate(time.Millisecond), r.RemoteAddr, payload)
			return
		}
		log.Printf("%s %s %d %s %s", r.Method, r.URL.Path, ww.status, time.Since(start).Truncate(time.Millisecond), r.RemoteAddr)
	})
}

// statusWriter captures the response status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func runHTTP(ctx context.Context, addr string, handler http.Handler, shutdownGrace time.Duration) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	return srv.ListenAndServe()
}

func runHTTPS(ctx context.Context, addr, certFile, keyFile string, handler http.Handler, shutdownGrace time.Duration) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	return srv.ListenAndServeTLS(certFile, keyFile)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func loadConfig() config {
	maxBodyBytes := envInt("HTTP_MAX_BODY_BYTES", 262144)
	if maxBodyBytes < 1024 {
		maxBodyBytes = 1024
	}

	httpAddr := envOrDefault("HTTP_ADDR", "0.0.0.0:8080")
	httpsAddr := envOrDefault("HTTPS_ADDR", "")
	httpsCertFile := strings.TrimSpace(os.Getenv("HTTPS_CERT_FILE"))
	httpsKeyFile := strings.TrimSpace(os.Getenv("HTTPS_KEY_FILE"))
	httpPath := envOrDefault("HTTP_PATH", "/ingest")
	fastlyPath := envOrDefault("HTTP_FASTLY_PATH", "/fastly")
	fastlyServiceID := strings.TrimSpace(os.Getenv("FASTLY_SERVICE_ID"))
	authToken := strings.TrimSpace(os.Getenv("HTTP_AUTH_TOKEN"))
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
		httpAddr:        httpAddr,
		httpsAddr:       httpsAddr,
		httpsCertFile:   httpsCertFile,
		httpsKeyFile:    httpsKeyFile,
		httpPath:        httpPath,
		fastlyPath:      fastlyPath,
		fastlyServiceID: fastlyServiceID,
		authToken:       authToken,
		maxBodyBytes:    maxBodyBytes,
		flushTimeout:    flushTimeout,
		shutdownGrace:   shutdownGrace,
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
