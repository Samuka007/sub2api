# Changelog

## 1.1.26 - 2026-09-15

### Added

- Add DeepSeek Vision content moderation for JPEG, PNG, GIF, and WebP inputs with bounded image decoding and SSRF-safe remote image handling.

### Fixed

- Preserve image-source and adapter-capacity failures without freezing healthy moderation keys.
- Reject remote images in pre-block mode when the audited bytes cannot be bound to the downstream request.

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
