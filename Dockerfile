# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags "-s -w" -o /out/http-to-sentry-go ./

FROM alpine:3.19
RUN addgroup -S app && adduser -S app -G app \
	&& apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/http-to-sentry-go /app/http-to-sentry-go

EXPOSE 8080
USER app
ENTRYPOINT ["/app/http-to-sentry-go"]
