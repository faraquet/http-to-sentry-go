# http-to-sentry-go

Small Go service that receives HTTP log payloads and forwards each entry to Sentry.

## Environment

- `SENTRY_DSN` (required): Sentry DSN.
- `SENTRY_ENVIRONMENT` (optional): Sentry environment name.
- `SENTRY_RELEASE` (optional): Sentry release name.
- `SENTRY_FLUSH_TIMEOUT_MS` (optional, default `2000`): flush timeout on shutdown.
- `HTTP_ADDR` (optional, default `0.0.0.0:8080`): HTTP listen address.
- `HTTP_PATH` (optional, default `/ingest`): ingest path.
- `HTTP_MAX_BODY_BYTES` (optional, default `262144`): max request body size.
- `HTTP_SHUTDOWN_TIMEOUT_MS` (optional, default `5000`): graceful shutdown timeout.

## Payload format

Send a plain text body and it will be used as the Sentry message. If `Content-Type: application/json`, the body is parsed as:

```json
{
  "message": "string",
  "level": "debug|info|warning|error|fatal",
  "timestamp": "RFC3339",
  "tags": {"key": "value"},
  "extra": {"any": "json"}
}
```

## Local run

```bash
export SENTRY_DSN=your_dsn_here

go run ./
```

Send a JSON log:

```bash
curl -X POST http://127.0.0.1:8080/ingest \
  -H 'Content-Type: application/json' \
  -d '{"message":"hello","level":"info","tags":{"service":"api"}}'
```

Send a JSON log (full example):

```bash
curl -X POST http://127.0.0.1:8080/ingest \
  -H 'Content-Type: application/json' \
  -d '{
    "message": "order failed",
    "level": "error",
    "timestamp": "2026-01-29T12:34:56Z",
    "tags": {"service": "checkout", "env": "prod"},
    "extra": {"order_id": 1234, "amount": 49.99}
  }'
```

Send a text log:

```bash
curl -X POST http://127.0.0.1:8080/ingest \
  -d 'plain text log line'
```

## Docker

```bash
docker build -t http-to-sentry-go .

docker run --rm \
  -e SENTRY_DSN=your_dsn_here \
  -p 8080:8080 \
  http-to-sentry-go
```
