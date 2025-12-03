# Code Improvements and Modernization Plan

This document outlines opportunities for modernizing and simplifying the `envoy-exporter` codebase.

## Future Improvements

### 1. Configuration Validation
**Issue:** Configuration validation is performed with ad-hoc `if` statements and `log.Fatal` in `main`.
**Solution:**
- Use a validation library like `github.com/go-playground/validator` or add a `Validate() error` method to the `Config` struct.
- Centralize validation logic.
- Return errors instead of logging fatal immediately, allowing `main` to decide how to exit.

### 2. Hardcoded Values and Magic Strings
**Issue:** Strings like "production", "total-consumption", "measurement-type" are hardcoded in multiple places. The netdata expvar port `:6666` is also hardcoded.
**Solution:**
- Define constants for all measurement types and tag keys.
- Make the expvar port configurable in `envoy.yaml`.

### 3. Structured Logging Improvements
**Issue:** `scrape` logs errors but continues.
**Solution:**
- Ensure all logs include relevant context (e.g., fields for which part of the scrape failed).
- Review log levels (some `Fatal` in `main` could be handling errors more gracefully).

### 4. Enhanced Test Coverage
**Issue:** Tests currently cover the extraction logic and basic scraping flow.
**Solution:**
- Add tests for `scrapeLoop` (now that it is more testable with context).
- Add tests for configuration loading.

## Completed Improvements

- **Refactor Global Configuration:** `cfg` is no longer a global variable; it is passed as a struct.
- **Reuse InfluxDB Client:** The InfluxDB client is initialized once in `main` and reused.
- **Context and Graceful Shutdown:** Added `context.Context` support and signal handling for graceful shutdown.
- **Dependency Injection:** `scrape` and `scrapeLoop` now accept dependencies (client, writer) via interfaces.