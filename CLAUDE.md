# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Go daemon that scrapes an Enphase Envoy solar gateway for production, consumption, inverter, and battery data, then writes InfluxDB v2 line-protocol points. Deployed via Docker alongside InfluxDB + Grafana.

## Commands

```bash
# Build
go build ./...

# Test (all)
go test -v ./...

# Test (single)
go test -v -run TestScrape_PartialErrors ./...

# Lint / format
go vet ./...
go fmt ./...
```

CI runs `go build ./...` and `go test -v ./...` on push to main.

## Architecture

The program is split across a small set of files:

- **`config.go`** — `Config` struct (YAML), `LoadConfig()`, `Validate()`. All optional fields default here (interval=30s, port=6666, retry=5s).
- **`collect.go`** — core scrape pipeline: `EnvoyClient`/`PointWriter`/`ClientFactory` interfaces, `scrape()`, `scrapeLoop()`, `connectWithBackoff()`, InfluxDB point-building helpers, `expvar` metric counters.
- **`main.go`** — wires everything: parses flags, loads config, authenticates with Enphase (JWT auto-fetch), starts expvar HTTP server, creates InfluxDB client, calls `scrapeLoop()`.

### Key interfaces (in `collect.go`)

```go
EnvoyClient  — Production() / Inverters() / Batteries() / InvalidateSession()
PointWriter  — WritePoint(ctx, ...point)
ClientFactory — func(cfg *Config) (EnvoyClient, error)
```

All three are defined for testability. Tests inject `MockEnvoyClient` (func-field struct) and `MockPointWriter` (captures written points).

### Scrape loop flow

```
scrapeLoop → connectWithBackoff (exponential, cap 5m)
           → immediate scrape, then ticker every cfg.Interval seconds
           → scrape() calls Production/Inverters/Batteries independently
             (partial errors logged, don't abort remaining endpoints)
           → WritePoint to InfluxDB blocking API
           → updates expvar counters + lastScrapeTime atomic
```

### InfluxDB measurement names

| Data | Measurement name |
|---|---|
| Per-phase production/consumption | `production-line<N>`, `consumption-line<N>`, `net-line<N>` |
| Per-inverter | `inverter-production-<SERIAL>` |
| Per-battery | `battery-<SERIAL>` |

All points carry `source`, `measurement-type`, and relevant serial/index tags.

## Configuration

YAML file, path via `-config` flag (default: `envoy.yaml`). See `envoy.yaml.example` for a template. Auth: either `username`+`password` (JWT auto-fetched) or static `jwt`. Both can coexist to enable JWT refresh.

## Testing patterns

- `MockEnvoyClient` in `collect_test.go` uses function fields — set only the funcs needed per test, others return `nil, nil` by default.
- `MockPointWriter.Written` accumulates points for assertion.
- Use `context.WithTimeout` to bound `scrapeLoop` tests.
- Helper functions `tagMap(pt)` and `fieldMap(pt)` extract point metadata for assertions.

## Spec and open work

`spec.md` (repo root) is the authoritative design document. It defines the intended behavior including several improvements not yet implemented:
- JWT expiry parsing and proactive refresh
- `tls_insecure_skip_verify` config option for self-signed Envoy certs
- `/health` endpoint (currently only `/debug/vars` via expvar)
- Debug server must bind `0.0.0.0:<port>`, not `localhost` (Docker visibility bug)
