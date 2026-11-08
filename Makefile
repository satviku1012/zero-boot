# ZeroBoot developer tasks. Requires: go, protoc, protoc-gen-go, protoc-gen-go-grpc.

.PHONY: all proto build build-linux test test-integration vet tidy up down bench clean

all: proto build

## proto: regenerate Go bindings next to proto/zeroboot.proto
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
	       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
	       proto/zeroboot.proto

## build: compile all four binaries for the host OS (Linux isolation is stubbed on darwin)
build:
	go build -o bin/ ./cmd/...

## build-linux: cross-compile for the Docker worker image
build-linux:
	GOOS=linux GOARCH=$${GOARCH:-amd64} CGO_ENABLED=0 go build -o bin/linux/ ./cmd/...

## test: unit tests (host-safe)
test:
	go test ./...

## test-integration: Linux-only tests needing root + writable cgroup v2
test-integration:
	go test -tags=integration -count=1 ./test/integration/...

vet:
	go vet ./...

tidy:
	go mod tidy

up:
	docker compose up --build -d

down:
	docker compose down

bench:
	./scripts/benchmark.sh

clean:
	rm -rf ./bin
