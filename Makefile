BIN_DIR := bin
SERVER  := $(BIN_DIR)/whim-server
CLIENT  := $(BIN_DIR)/whim-client

.PHONY: all build server client run-server run-client clean test vet fmt

all: build

build: server client

server:
	go build -o $(SERVER) ./cmd/server

client:
	go build -o $(CLIENT) ./cmd/client

run-server:
	go run ./cmd/server --addr :9000

run-client:
	go run ./cmd/client --server ws://localhost:9000 --channel myapp --target http://localhost:8080

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf $(BIN_DIR)
