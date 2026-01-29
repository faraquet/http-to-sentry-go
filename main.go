package main

import (
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
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
)

type config struct {
	httpAddr      string
	httpPath      string
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

func main() {
	cfg := loadConfig()

	if err := initSentry(); err != nil {
		log.Fatalf("sentry init: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.httpPath, func(w http.ResponseWriter, r *http.Request) {
		handleIngest(w, r, cfg)
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

	log.Printf("ready: http=%s path=%s", cfg.httpAddr, cfg.httpPath)
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
	if !strings.HasPrefix(httpPath, "/") {
		httpPath = "/" + httpPath
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
			if ts, err := time.Parse(time.RFC3339, parsedPayload.Timestamp); err == nil {
				event.Timestamp = ts
			}
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
