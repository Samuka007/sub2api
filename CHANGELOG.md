# Changelog

## 2026-07-31

### Changed

- Allow model request tracing to export to a generic OTLP/HTTP Collector without requiring Langfuse credentials or adding Langfuse-specific headers.

## 2026-07-29

### Added

- Add optional, dynamically reloadable model request tracing to self-hosted Langfuse over OTLP/HTTP, with bounded queues and retries, UTF-8-safe content limits, and fail-open Collector outage handling.

## 2026-07-28

### Fixed

- Hold automatic billing for reported text usage that exceeds configured model input or output limits, while retaining an auditable usage record.
- Cap unified balance deductions at the user's available balance and record the amount actually charged, preventing new negative balances.
