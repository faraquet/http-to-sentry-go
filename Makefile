IMAGE ?= faraquet/http-to-sentry-go
VERSION ?= 0.1.7

.PHONY: build push release

build:
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

push:
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

release: build push
