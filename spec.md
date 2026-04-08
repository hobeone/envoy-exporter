# envoy-exporter: Specification

A Go daemon that scrapes an Enphase Envoy solar gateway for production, consumption, inverter, and battery data, then writes those metrics to InfluxDB v2.

---

## Overview

The exporter runs as a long-lived daemon. On a configurable interval it authenticates with the local Envoy gateway, fetches power metrics, and writes InfluxDB line-protocol points. It exposes a debug HTTP server and is packaged as a Docker image for deployment alongside InfluxDB + Grafana.

---

## Configuration

Configuration is a YAML file. Path is supplied via `-config` flag (default: `envoy.yaml`).

### Required Fields

| Field | YAML Key | Description |
|---|---|---|
| Gateway address | `address` | Local URL of the Envoy gateway, e.g. `https://192.168.20.135` |
| Serial number | `serial` | Envoy device serial number |
| InfluxDB URL | `influxdb` | e.g. `http://influxdb:8086` |
| InfluxDB token | `influxdb_token` | Auth token |
| InfluxDB org | `influxdb_org` | Organization name |
| InfluxDB bucket | `influxdb_bucket` | Destination bucket |

**Authentication** — one of the following:

- `username` + `password` (Enphase Enlighten account credentials). JWT is auto-fetched at startup.
- `jwt` (a pre-obtained JWT token). Username/password are not needed if this is provided.

If `username`+`password` are present but `jwt` is missing, the exporter fetches a JWT at startup and uses it for the session. The token is **not** persisted back to the config file.

### Optional Fields

| Field | YAML Key | Default | Description |
|---|---|---|---|
| Scrape interval | `interval` | `5` | Seconds between scrapes |
| Debug server port | `expvar_port` | `6666` | Port for the expvar HTTP server |
| Retry interval | `retry_interval` | `5` | Seconds between connection retries |
| Source tag | `source` | `""` | Value of the `source` tag on all data points |

### CLI Flags

| Flag | Default | Description |
|---|---|---|
| `-config` | `envoy.yaml` | Path to YAML config file |
| `-debug` | `false` | Enable debug-level log output |

---

## Authentication

The Envoy gateway requires a JWT for local API access. There are two ways to supply one:

### Manual JWT

Obtain a token by visiting:
```
https://enlighten.enphaseenergy.com/entrez-auth-token?serial_num=<SERIAL>
```
and pasting the result into `jwt:` in the config file.

### Auto-fetch

If `username` and `password` are set and `jwt` is empty, the exporter authenticates via the Enphase Enlighten web login flow:

1. HTTP POST to `https://enlighten.enphaseenergy.com/login/login` with credentials (form-encoded). Cookies are maintained via a cookie jar.
2. HTTP GET to `https://enlighten.enphaseenergy.com/entrez-auth-token?serial_num=<SERIAL>`. The response body is the raw JWT.

### Improvement: Runtime JWT Refresh

JWT tokens issued by Enphase have a finite lifetime. The exporter **must** parse the `exp` claim from the JWT at startup and proactively refresh the token before expiry. Suggested behavior:

- After fetching or loading a JWT, decode the `exp` claim (no signature verification needed for parsing).
- Schedule a refresh when less than a configurable lead time remains (default: 1 hour before expiry).
- On refresh: re-run the auto-fetch flow using `username`+`password`. If refresh fails, log an error and retry on the next interval.
- If only a static JWT is provided (no credentials), log a warning at startup that token expiry will not be handled.

This requires `username` + `password` to be present even when `jwt` is also set.

---

## Data Collection

### Envoy Client

The exporter uses the `github.com/loafoe/go-envoy` library as a thin client for the Envoy local API. Three endpoints are scraped each interval:

| Method | Data |
|---|---|
| `Production()` | Per-phase production and consumption power |
| `Inverters()` | Per-microinverter last-reported wattage |
| `Batteries()` | Per-battery charge percentage and temperature |

Errors from any individual endpoint are logged but do not abort the scrape — remaining endpoints are still attempted and partial results are written.

### Improvement: TLS Skip-Verify Option

Envoy gateways use self-signed TLS certificates. The current code relies on system CA trust, which will cause TLS errors for most users. A new config field `tls_insecure_skip_verify: true` (default `false`) should allow the HTTP client (and the `go-envoy` client) to skip certificate verification when connecting to the gateway. This should be limited to the gateway connection only, not the Enphase cloud auth calls.

---

## InfluxDB Output

All points are written using the InfluxDB v2 blocking write API. Every point is timestamped with `time.Now()` at scrape time.

### Measurement Schema

**Production / Consumption lines**

Measurement name: `<type>-line<idx>` where `<type>` is `production`, `consumption`, or `net`.

| Tag | Value |
|---|---|
| `source` | Config `source` value |
| `measurement-type` | `production`, `total-consumption`, or `net-consumption` |
| `line-idx` | Phase index (0-based) |

| Field | Unit | Description |
|---|---|---|
| `P` | W | Real power (`WNow`) |
| `Q` | VAR | Reactive power |
| `S` | VA | Apparent power |
| `I_rms` | A | RMS current |
| `V_rms` | V | RMS voltage |

**Inverter production**

Measurement name: `inverter-production-<SERIAL>`

| Tag | Value |
|---|---|
| `source` | Config `source` value |
| `measurement-type` | `inverter` |
| `serial` | Inverter serial number |

| Field | Unit | Description |
|---|---|---|
| `P` | W | Last reported watts |

**Battery**

Measurement name: `battery-<SERIAL>`

| Tag | Value |
|---|---|
| `source` | Config `source` value |
| `measurement-type` | `battery` |
| `serial` | Battery serial number |

| Field | Unit | Description |
|---|---|---|
| `percent-full` | % | State of charge |
| `temperature` | °C | Battery temperature |

---

## Scrape Loop

1. **Connect:** Call the client factory to create an authenticated Envoy client. Retry on failure with a fixed interval (`retry_interval`).
2. **Immediate scrape:** Perform one scrape immediately on successful connect (don't wait for the first tick).
3. **Periodic scrape:** Tick every `interval` seconds. Each iteration logs scrape duration and point count.
4. **Shutdown:** On SIGINT or SIGTERM the context is cancelled, the scrape loop exits cleanly.

### Improvement: Exponential Backoff on Retry

The initial connection retry uses a fixed interval. Replace with exponential backoff: start at `retry_interval`, double on each failure, cap at 5 minutes. Reset to `retry_interval` after a successful scrape.

---

## Debug HTTP Server

An HTTP server is started on startup serving Go's built-in `expvar` metrics at `/debug/vars`. It exposes Go runtime stats (memory, GC, goroutines) in JSON.

### Bug Fix: Docker Binding

The current implementation binds to `localhost:<port>`. Inside a Docker container, `localhost` only accepts loopback connections — the compose port mapping (`"6666:6666"`) has no effect. **The server must bind to `0.0.0.0:<port>`** to be reachable from outside the container.

### Improvement: Health Endpoint

Add a `/health` endpoint on the same server returning `200 OK` with body `ok` when the exporter is running normally, and `503 Service Unavailable` when the last scrape failed or no successful scrape has occurred within `3 × interval` seconds. This enables Docker `HEALTHCHECK` and orchestration readiness probes.

### Improvement: Exporter Self-Metrics via expvar

Publish the following counters/gauges to `expvar` so they appear at `/debug/vars`:

| Name | Type | Description |
|---|---|---|
| `scrape_total` | counter | Total scrape attempts |
| `scrape_errors` | counter | Scrapes with at least one endpoint error |
| `points_written_total` | counter | Cumulative InfluxDB points written |
| `last_scrape_duration_ms` | gauge | Duration of the most recent scrape in ms |
| `last_scrape_time` | gauge | Unix timestamp of most recent successful scrape |

---

## Deployment

### Docker

Multi-stage build on `golang:1.25.1-alpine` + `alpine:latest`. Runs as non-root user `envoy-exporter`.

- Binary: `/usr/bin/envoy-exporter`
- Config: `/etc/envoy-exporter/envoy.yaml` (mount as read-only volume)
- Exposed port: `6666`

```yaml
# docker-compose.yml (excerpt)
services:
  envoy-exporter:
    image: envoy-exporter:latest
    restart: unless-stopped
    volumes:
      - ./envoy.yaml:/etc/envoy-exporter/envoy.yaml:ro
    ports:
      - "6666:6666"
    networks:
      - nginx_proxy_network
```

The `nginx_proxy_network` external network is used for reverse proxy integration.

### Systemd

A service unit is provided for bare-metal deployment:

- User/Group: `hobe:hobe`
- Type: `simple`
- Restart: `on-failure`, limited to 2 failures per 30 seconds
- Requires: `network-online.target`

---

## Error Handling

| Scenario | Behavior |
|---|---|
| Config file missing or unparseable | Fatal: logged, process exits |
| Config validation failure | Fatal: logged, process exits |
| JWT auto-fetch failure | Non-fatal warning; continues if a static JWT is present; exits if no auth is available |
| Envoy connection failure at startup | Retry with backoff until context cancelled |
| Individual scrape endpoint error | Log error, continue with remaining endpoints |
| InfluxDB write error | Log error, discard points, continue |
| Signal (SIGINT/SIGTERM) | Cancel context, drain in-flight scrape, exit cleanly |

---

## Code Architecture

```
main()
  └─ run(args)
       ├─ LoadConfig() → Config
       ├─ AuthenticateWithEnphase() [if needed]
       ├─ go: expvar HTTP server (graceful shutdown on ctx cancel)
       ├─ influxdb2.NewClient() → WriteAPIBlocking
       └─ scrapeLoop(ctx, cfg, writeAPI, clientFactory)
            ├─ clientFactory(cfg) → EnvoyClient  [retry loop]
            └─ tick loop:
                 └─ scrape(ctx, client, writeAPI, sourceTag)
                      ├─ client.Production() → extractProductionStats()
                      ├─ client.Inverters()  → extractInverterStats()
                      └─ client.Batteries() → extractBatteryStats()
```

### Key Interfaces

```go
// EnvoyClient abstracts the go-envoy library for testability.
type EnvoyClient interface {
    Production() (*envoy.ProductionResponse, error)
    Inverters() (*[]envoy.Inverter, error)
    Batteries() (*[]envoy.Battery, error)
    InvalidateSession()
}

// PointWriter abstracts the InfluxDB blocking write API for testability.
type PointWriter interface {
    WritePoint(ctx context.Context, point ...*influxdb2write.Point) error
}

// ClientFactory enables injecting mock clients in tests.
type ClientFactory func(cfg *Config) (EnvoyClient, error)
```

---

## Testing

Tests live in `main_test.go`. Coverage target: ≥ 85%.

Key test patterns:
- `MockEnvoyClient` — struct with function fields implementing `EnvoyClient`; errors and return values are set per test case
- `MockPointWriter` — captures written points for assertion
- HTTP test server used to mock Enphase auth endpoints in `TestAuthenticateWithEnphase`
- `context.WithTimeout` used to bound `scrapeLoop` tests

Areas requiring test coverage for new improvements:
- JWT expiry parsing and refresh scheduling
- Health endpoint response under normal and degraded conditions
- Exponential backoff retry sequencing
- `0.0.0.0` binding verification (or at least that the addr string is correct)

---

## Dependencies

| Package | Purpose |
|---|---|
| `github.com/influxdata/influxdb-client-go/v2` | InfluxDB v2 write client |
| `github.com/loafoe/go-envoy` | Enphase Envoy local API client |
| `gopkg.in/yaml.v3` | Config file parsing |
| `github.com/stretchr/testify` | Test assertions |
| `log/slog` (stdlib) | Structured logging |
| `expvar` (stdlib) | Runtime debug metrics |

---

## Open Issues / Deferred

- **Prometheus endpoint:** The name "exporter" conventionally implies a `/metrics` Prometheus endpoint. Currently only InfluxDB is supported. A future iteration could add a Prometheus text-format scrape endpoint, making InfluxDB optional.
- **Config hot-reload:** Reloading config (especially a refreshed JWT) without restarting the process is not implemented.
- **Multiple gateways:** Only a single Envoy gateway is supported per process instance.
