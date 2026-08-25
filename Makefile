GOTOOLCHAIN ?= local
IMAGE ?= manjuflow-studio
export GOTOOLCHAIN

.PHONY: build test race vet fmt check run docker-amd64 docker-arm64 docker

build:
	go build ./...

test:
	go test ./... -count=1

race:
	go test -race ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -l .

check: fmt vet build test race

run:
	go run ./cmd/server

docker-amd64:
	docker buildx build --platform linux/amd64 -t $(IMAGE):amd64 --load .

docker-arm64:
	docker buildx build --platform linux/arm64 -t $(IMAGE):arm64 --load .

docker: docker-amd64 docker-arm64
