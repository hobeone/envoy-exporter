# Code Improvements and Modernization Plan

This document outlines opportunities for modernizing and simplifying the `envoy-exporter` codebase.

## Future Improvements

### 1. Enhanced Test Coverage
**Issue:** Tests currently cover the extraction logic and basic scraping flow.
**Solution:**
- Add tests for `scrapeLoop` (now that it is more testable with context).
- Add tests for configuration loading.

## Completed Improvements

- **Refactor Global Configuration:** `cfg` is no longer a global variable; it is passed as a struct.
- **Reuse InfluxDB Client:** The InfluxDB client is initialized once in `main` and reused.
- **Context and Graceful Shutdown:** Added `context.Context` support and signal handling for graceful shutdown.
- **Dependency Injection:** `scrape` and `scrapeLoop` now accept dependencies (client, writer) via interfaces.
- **Configuration Validation:** Added a `Validate()` method to the `Config` struct to centralize validation logic and improved tests.
- **Hardcoded Values:** Replaced magic strings with constants and made the expvar port configurable.
- **Structured Logging:** Migrated from `logrus` to the standard library `log/slog`, implementing structured logging with contextual fields.
