BIN_DIR     := bin
SERVER      := $(BIN_DIR)/whim-server
CLIENT      := $(BIN_DIR)/whim-client
WEB_CLIENTS := internal/web/clients

VERSION ?= 0.0.1
GIT_VERSION := $(shell git -C . describe --tags --always --dirty 2>/dev/null)
ifneq ($(GIT_VERSION),)
  VERSION := $(GIT_VERSION)
endif
LDFLAGS      := -ldflags "-X main.version=$(VERSION)"
# Distribution builds: strip debug info and disable CGO for static/portable binaries.
DIST_LDFLAGS := -ldflags "-X main.version=$(VERSION) -s -w"

.PHONY: all build server client clients run-server run-client clean test coverage vet fmt tag

all: build

build: server client

# Cross-compile the client for all supported platforms and embed in the server.
clients:
	@mkdir -p $(WEB_CLIENTS)
	@for pair in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		os=$$(echo $$pair | cut -d/ -f1); \
		arch=$$(echo $$pair | cut -d/ -f2); \
		ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "  building client $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(DIST_LDFLAGS) \
			-o $(WEB_CLIENTS)/whim-client-$$os-$$arch$$ext \
			./cmd/client; \
	done

# Build server after cross-compiling clients so they get embedded.
server: clients
	go build $(LDFLAGS) -o $(SERVER) ./cmd/server

# Build client for the current platform only.
client:
	go build $(LDFLAGS) -o $(CLIENT) ./cmd/client

run-server:
	./$(SERVER) --addr :9000

run-client:
	./$(CLIENT) --server ws://localhost:9000 --channel myapp --target http://localhost:8080

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
	rm -f $(WEB_CLIENTS)/whim-client-*

tag:
	git tag -a $(VERSION) -m "release $(VERSION)"
