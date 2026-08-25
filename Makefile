GO ?= go
export GOTOOLCHAIN := local
IMAGE ?= manjuflow-studio

.PHONY: help build run test race vet fmt fmt-check measure docker-amd64 docker-arm64 clean

help:
	@echo "build         compile every package"
	@echo "run           start the HTTP server with a local SQLite file"
	@echo "test          run the full suite once"
	@echo "race          run the full suite under the race detector"
	@echo "vet           run go vet"
	@echo "fmt           format the tree"
	@echo "fmt-check     fail when a file is unformatted"
	@echo "measure       report Go production and test size"
	@echo "docker-amd64  build the linux/amd64 image"
	@echo "docker-arm64  build the linux/arm64 image"

build:
	$(GO) build ./...

run:
	MANJU_DB_PATH=./data/manjuflow.sqlite $(GO) run ./cmd/server

test:
	$(GO) test ./... -count=1

race:
	$(GO) test -race ./... -count=1

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

measure:
	$(GO) run ../../.agents/skills/go-base-project-create/scripts/measure_project.go -root .

docker-amd64:
	docker build --platform linux/amd64 -t $(IMAGE):amd64 .

docker-arm64:
	docker build --platform linux/arm64 -t $(IMAGE):arm64 .

clean:
	rm -rf data
