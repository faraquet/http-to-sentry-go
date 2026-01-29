# http-to-sentry-go

Small Go service that receives HTTP log payloads and forwards each entry to Sentry.

## Environment

- `SENTRY_DSN` (required): Sentry DSN.
- `SENTRY_ENVIRONMENT` (optional, default `development`): Sentry environment name.
- `SENTRY_RELEASE` (optional): Sentry release name.
- `SENTRY_FLUSH_TIMEOUT_MS` (optional, default `2000`): flush timeout on shutdown.
- `HTTP_ADDR` (optional, default `0.0.0.0:8080`): HTTP listen address.
- `HTTP_PATH` (optional, default `/ingest`): generic ingest path.
- `HTTP_FASTLY_PATH` (optional, default `/fastly`): Fastly events path.
- `HTTP_AUTH_TOKEN` (optional): if set, require `Authorization: Bearer <token>` for ingest endpoints.
- `HTTP_MAX_BODY_BYTES` (optional, default `262144`): max request body size.
- `HTTP_SHUTDOWN_TIMEOUT_MS` (optional, default `5000`): graceful shutdown timeout.

## Payload format

### Generic ingest (`HTTP_PATH`)

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

### Fastly events (`HTTP_FASTLY_PATH`)

Accepts Fastly event JSON objects (or arrays). Example fields:

```json
{
  "timestamp": "2026-01-29T11:41:12+0000",
  "client_ip": "203.0.113.10",
  "geo_country": "exampleland",
  "geo_city": "exampleville",
  "host": "example.com",
  "url": "/path/to/resource",
  "original_url": "",
  "request_method": "POST",
  "request_protocol": "HTTP/2",
  "request_referer": "https://example.com/previous",
  "request_user_agent": "Mozilla/5.0 (Example OS; Example Arch) ExampleBrowser/1.0",
  "response_state": "ERROR",
  "response_status": 503,
  "response_reason": "origin timeout",
  "response_body_size": 444,
  "tls_client_ja3_md5": "00000000000000000000000000000000",
  "fastly_server": "cache-example-XYZ",
  "fastly_is_edge": true
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
    "tags": {"service": "checkout"},
    "extra": {"order_id": 1234, "amount": 49.99}
  }'
```

Send a Fastly event:

```bash
curl -X POST http://127.0.0.1:8080/fastly \
  -H 'Content-Type: application/json' \
  -d '{
    "timestamp": "2026-01-29T11:41:12+0000",
    "client_ip": "203.0.113.10",
    "geo_country": "exampleland",
    "geo_city": "exampleville",
    "host": "example.com",
    "url": "/path/to/resource",
    "original_url": "",
    "request_method": "POST",
    "request_protocol": "HTTP/2",
    "request_referer": "https://example.com/previous",
    "request_user_agent": "Mozilla/5.0 (Example OS; Example Arch) ExampleBrowser/1.0",
    "response_state": "ERROR",
    "response_status": 503,
    "response_reason": "origin timeout",
    "response_body_size": 444,
    "tls_client_ja3_md5": "00000000000000000000000000000000",
    "fastly_server": "cache-example-XYZ",
    "fastly_is_edge": true
  }'
```

Send a text log:

```bash
curl -X POST http://127.0.0.1:8080/ingest \
  -d 'plain text log line'
```

## Release

```bash
make release VERSION=v0.1.0
make release IMAGE=faraquet/http-to-sentry-go VERSION=v0.1.0
```

## Docker

```bash
docker build -t http-to-sentry-go .

docker run --rm \
  -e SENTRY_DSN=your_dsn_here \
  -p 8080:8080 \
  http-to-sentry-go
```
