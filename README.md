# http-to-sentry-go

Small Go service that receives HTTP log payloads and forwards each entry to Sentry. It uses the official Sentry Go SDK and runs as a lightweight HTTP ingest service with optional Fastly log support.

## Environment

- `SENTRY_DSN` (required): Sentry DSN.
- `SENTRY_ENVIRONMENT` (optional, default `development`): Sentry environment name.
- `SENTRY_RELEASE` (optional): Sentry release name.
- `SENTRY_FLUSH_TIMEOUT_MS` (optional, default `2000`): flush timeout on shutdown.
- `HTTP_ADDR` (optional, default `0.0.0.0:8080`): HTTP listen address.
- `HTTP_PATH` (optional, default `/ingest`): generic ingest path.
- `HTTP_FASTLY_PATH` (optional, default `/fastly`): Fastly events path.
- `FASTLY_SERVICE_ID` (optional): required to answer Fastly HTTPS logging verification challenge.
  Fastly endpoints are only enabled when this is set.
- `HTTP_AUTH_TOKEN` (optional): if set, require `Authorization: Bearer <token>` for ingest endpoints.
- `HTTP_MAX_BODY_BYTES` (optional, default `1048576`): max request body size.
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
Fastly routes are enabled only when `FASTLY_SERVICE_ID` is set. If it is empty, the Fastly ingest and challenge endpoints are not registered.
Fastly routes are enabled only when `FASTLY_SERVICE_ID` is set.

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

### Fastly verification challenge

Fastly sends a GET to `/.well-known/fastly/logging/challenge`. If `FASTLY_SERVICE_ID` is set, this endpoint responds with the hex SHA-256 of the service ID on its own line.

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

## Tests

```bash
make test
```

## Release

```bash
make release VERSION=0.2.4
make release IMAGE=faraquet/http-to-sentry-go VERSION=0.2.4
```

## Docker

```bash
docker build -t http-to-sentry-go .

docker run --rm \
  -e SENTRY_DSN=your_dsn_here \
  -p 8080:8080 \
  http-to-sentry-go
```

## License

```
MIT License

Copyright (c) 2026 Andrei Andriichuk

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
