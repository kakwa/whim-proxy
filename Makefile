BIN_DIR := bin
SERVER  := $(BIN_DIR)/whim-server
CLIENT  := $(BIN_DIR)/whim-client

VERSION ?= 0.0.1
GIT_VERSION := $(shell git -C . describe --tags --always --dirty 2>/dev/null)
ifneq ($(GIT_VERSION),)
  VERSION := $(GIT_VERSION)
endif
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: all build server client run-server run-client clean test coverage vet fmt tag

all: build

build: server client

server:
	go build $(LDFLAGS) -o $(SERVER) ./cmd/server

client:
	go build $(LDFLAGS) -o $(CLIENT) ./cmd/client

run-server:
	go run ./cmd/server --addr :9000

run-client:
	go run ./cmd/client --server ws://localhost:9000 --channel myapp --target http://localhost:8080

test:
	go test -race ./...

coverage:
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "report: coverage.html"

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf $(BIN_DIR)

tag:
	git tag -a $(VERSION) -m "release $(VERSION)"
