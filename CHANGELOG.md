# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

## [0.1.9] - 2026-06-13

### Added
- `--version` flag on server and client binaries

## [0.1.8] - 2026-06-13

### Fixed
- Server now rejects webhooks carrying the `X-Whim-Proxy-Client` header, preventing an amplification loop when a whim-client target URL points back at the whim-server

## [0.1.7] - 2026-06-13

### Changed
- Updated logo colour to blue

## [0.1.6] - 2026-06-13

### Changed
- Home page footer: centred layout with a horizontal rule

## [0.1.5] - 2026-06-13

### Changed
- Improved home page layout and quick-start content
- Fixed `X-Forwarded-Proto` header in nginx reverse-proxy config (now applied to all proxy locations)
- Added example test webhook consumer script under `misc/`

## [0.1.4] - 2026-06-13

### Fixed
- Client forwards replayed requests to `--target` directly, ignoring `event.Path`; this prevents double-path issues when the target already includes a path prefix

## [0.1.3] - 2026-06-13

### Changed
- `logo.svg` is now the single source of truth for the server favicon (embedded at build time via `go:embed`)

## [0.1.2] - 2026-06-13

### Added
- Client logs the full event payload (headers + body) at `debug` level

## [0.1.1] - 2026-06-13

### Added
- Public instance URL documented in README

## [0.1.0] - 2026-06-13

Initial release.

### Added
- WebSocket pub/sub proxy: `/hook/{channel}` receives webhooks; `/subscribe/{channel}` streams them to connected clients
- `/logs/{channel}` endpoint returns the last N events per channel
- In-memory event store (default) and optional Redis store (`--redis-url`, `--redis-ttl`)
- Per-channel webhook rate limiting (`--max-hook-rate`, `--max-hook-burst`)
- Channel and subscriber caps (`--max-channels`, `--max-clients`, `--max-clients-per-ip`)
- UUID-based channels — `--gen-uuid` flag on client generates a fresh channel ID
- `--logs` flag on client fetches and pretty-prints recent events then exits
- Client sends `X-Whim-Proxy-Client: <version>` on WebSocket connect and on each replayed request; server logs the client version
- Structured logging via [zap](https://github.com/uber-go/zap) with configurable level (`--log-level`) and JSON format (`--json`)
- Server embeds cross-compiled client binaries for Linux, macOS, and Windows (amd64 + arm64); served from the home page
- Home page with quick-start guide and client download links
- nginx reverse-proxy config and systemd unit file under `misc/`
- GitHub Actions CI with race-detector tests and Codecov coverage reporting
