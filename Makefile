IMAGE ?= faraquet/http-to-sentry-go
VERSION ?= 0.2.1

.PHONY: build push release test

build:
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

push:
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

release: build push

test:
	go test ./...
