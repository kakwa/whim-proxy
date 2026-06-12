# whim-proxy

[![CI](https://github.com/kakwa/whim-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/kakwa/whim-proxy/actions/workflows/ci.yml)
[![Coverage](https://codecov.io/gh/kakwa/whim-proxy/branch/main/graph/badge.svg)](https://codecov.io/gh/kakwa/whim-proxy)

A lightweight webhook proxy with a publish/subscribe architecture. Send webhooks
to the **server**; one or more **clients** receive and replay them against a
local HTTP target. Ideal for exposing a local service to external webhook
senders without a full tunnel.

## Quick start

```bash
# 1. Start the proxy server (listens on :9000 by default)
go run ./cmd/server --addr :9000

# 2. Start a client that subscribes to "myapp" and forwards to localhost:8080
go run ./cmd/client --server ws://localhost:9000 --channel myapp --target http://localhost:8080

# 3. Send a test webhook
curl -X POST http://localhost:9000/hook/myapp \
     -H "Content-Type: application/json" \
     -d '{"event":"ping"}'
```

The client will replay the request to `http://localhost:8080/hook/myapp` with
the original method, path, query string, headers, and body intact.

## Flags

### Server

| Flag     | Default  | Description          |
|----------|----------|----------------------|
| `--addr` | `:9000`  | TCP listen address   |

### Client

| Flag        | Default                  | Description                            |
|-------------|--------------------------|----------------------------------------|
| `--server`  | `ws://localhost:9000`    | WebSocket server base URL              |
| `--channel` | *(required)*             | Channel name to subscribe to           |
| `--target`  | `http://localhost:8080`  | Local HTTP service to forward events to |

## Architecture

```mermaid
graph TD
    subgraph Public Internet
        WS[Webhook Sender<br/>e.g. GitHub / Stripe]
    end

    subgraph Public Server
        SRV[whim-proxy server<br/>:9000]
        CH1[channel: myapp]
        CH2[channel: payments]
        SRV --> CH1
        SRV --> CH2
    end

    subgraph Developer Machine A
        CLA[whim-proxy client<br/>--channel myapp]
        LA[Local Service<br/>localhost:8080]
        CLA -->|HTTP replay| LA
    end

    subgraph Developer Machine B
        CLB[whim-proxy client<br/>--channel payments]
        LB[Local Service<br/>localhost:3000]
        CLB -->|HTTP replay| LB
    end

    WS -->|POST /hook/myapp| SRV
    CH1 -->|WebSocket broadcast| CLA
    CH2 -->|WebSocket broadcast| CLB
```

## Sequence

```mermaid
sequenceDiagram
    participant Sender as Webhook Sender
    participant Server as whim-proxy server
    participant Client as whim-proxy client
    participant Local as Local Service

    Client->>Server: GET /subscribe/{channel} (WebSocket upgrade)
    Server-->>Client: 101 Switching Protocols

    Note over Client,Server: connection held open

    Sender->>Server: POST /hook/{channel}<br/>headers + body
    Server-->>Sender: 200 OK

    Server->>Server: serialise to WebhookEvent JSON<br/>{id, method, path, query, headers, body}
    Server->>Client: WebSocket message (WebhookEvent JSON)

    Client->>Client: deserialise event
    Client->>Local: POST {target}{path}?{query}<br/>original headers + body
    Local-->>Client: 200 OK

    Note over Client,Server: if connection drops
    Client->>Server: reconnect with exponential backoff
```

## How it works

1. The server receives HTTP POST requests at `/hook/{channel}`.
2. It serialises the full request (method, path, query, headers, body) into a
   `WebhookEvent` JSON message.
3. All WebSocket clients subscribed to that channel receive the message.
4. Each client re-issues the request verbatim to its configured `--target`.
5. The client auto-reconnects with exponential backoff if the server drops.

## Building

```bash
go build ./...
# Produces: cmd/server and cmd/client binaries
```
