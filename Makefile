REGISTRY ?= harbor.thorix.io/library
IMAGE_NAME ?= vrestic
TAG ?= latest

IMAGE := $(REGISTRY)/$(IMAGE_NAME):$(TAG)

.PHONY: build docker-build docker-push scan scan-full all

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o vrestic .

docker-build:
	docker build -t $(IMAGE) .

docker-push:
	docker push $(IMAGE)

scan:
	trivy image --severity HIGH,CRITICAL $(IMAGE)

scan-full:
	trivy image $(IMAGE)

all: docker-build scan docker-push
