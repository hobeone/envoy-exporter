# Initial Concept
A Go-based metrics exporter that scrapes data from an Enphase Envoy solar system and writes it to InfluxDB.

# Product Guide

## Target Users
- Home automation enthusiasts using InfluxDB/Grafana to visualize their home energy data.

## Goals
- Provide real-time visibility into solar production and consumption through a reliable data pipeline.

## Key Features
- Automated scraping of production and consumption data from Enphase Envoy hardware.
- Detailed support for Inverter-level and Battery-level metrics.
- Configurable reporting intervals and graceful shutdown support for stable service operation.

## Non-Functional Requirements
- **Reliability:** High stability for long-running service operation.
- **Efficiency:** Low resource footprint (CPU/Memory) to run on low-power home servers.
- **Observability:** Comprehensive logging (structured with `slog`) and error handling for troubleshooting.

## Evolution Strategy
- **Developer-focused:** Prioritize refactoring, modern Go patterns, and codebase cleanliness to ensure long-term maintainability.
