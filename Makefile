IMAGE ?= faraquet/http-to-sentry-go
VERSION ?= v0.1.0

.PHONY: build push release

build:
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

push:
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

release: build push
