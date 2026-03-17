# Envoy Exporter

`envoy-exporter` is a Go daemon that scrapes production and consumption data from an Enphase Envoy gateway and writes it to InfluxDB.

## Features
- Scrapes production, consumption, battery, and inverter data.
- Writes data to InfluxDB (v2).
- Supports JWT authentication for Enphase gateways.
- Provides an `expvar` server for monitoring.
- Lightweight Docker image based on Alpine.

## Configuration

The application requires a YAML configuration file. See `envoy.yaml.example` for a template.

| Key | Description |
| --- | --- |
| `username` | Enphase Enlighten email |
| `password` | Enphase Enlighten password |
| `address` | Local IP or hostname of the Envoy gateway |
| `serial` | Envoy gateway serial number |
| `influxdb` | URL of the InfluxDB instance (e.g., `http://localhost:8086`) |
| `influxdb_token` | InfluxDB authentication token |
| `influxdb_org` | InfluxDB organization name |
| `influxdb_bucket` | InfluxDB bucket name |
| `interval` | Scrape interval in seconds (default: 5) |
| `source` | Tag to add to all points (e.g., `solar-system-1`) |

## Running with Docker

### 1. Create your configuration
Copy `envoy.yaml.example` to `envoy.yaml` and fill in your details.

```bash
cp envoy.yaml.example envoy.yaml
# Edit envoy.yaml with your credentials and settings
```

### 2. Build and Run using Docker Compose
The easiest way to run the exporter is using Docker Compose:

```bash
docker compose up -d
```

This will build the image and start the container, mounting your `envoy.yaml` into the container at `/etc/envoy-exporter/envoy.yaml`.

### 3. Build and Run manually

To build the image:
```bash
docker build -t envoy-exporter .
```

To run the container:
```bash
docker run -d \
  --name envoy-exporter \
  -v $(pwd)/envoy.yaml:/etc/envoy-exporter/envoy.yaml:ro \
  -p 6666:6666 \
  envoy-exporter
```

## Monitoring
The `expvar` server is available on port `6666` (default). You can access it at `http://localhost:6666/debug/vars`.
