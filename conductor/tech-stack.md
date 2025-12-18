# Technology Stack

## Core Language & Runtime
- **Go (1.25+):** The primary programming language, utilizing modern features and the official toolchain.

## Data Management
- **InfluxDB v2:** Used as the primary time-series database for storing exported metrics.
- **`github.com/influxdata/influxdb-client-go/v2`:** The official Go client for InfluxDB interaction.

## Domain Specific Libraries
- **`github.com/loafoe/go-envoy`:** Utilized for communication and data extraction from Enphase Envoy devices.

## Infrastructure & Observability
- **`log/slog`:** Standard library structured logging for consistent and machine-readable logs.
- **`context`:** Standard library context for managing request lifecycles and graceful shutdowns.
- **`gopkg.in/yaml.v3`:** For parsing and validating project configuration.

## Testing & Quality
- **`github.com/stretchr/testify`:** Used for assertions and mocks in the test suite.
- **Dependency Injection:** Architectural pattern used to facilitate testing and modularity.
