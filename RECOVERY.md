# Source Recovery Record

This repository's `main` branch was reconstructed for collaborative development
from the production release described below.

## Production reference

- Image: `sub2api:pricing-model-iq-v0.1.160-v2`
- Runtime version: `0.1.160+pricing-radar.model-iq.2`
- Image source marker: `2c0dbd7f8a23c1080095c8f7ebd967ee7ff666030`
- Recovery date: 2026-07-18

The source marker above is preserved only as provenance. That Git object and the
exact source tree used to build the image were no longer available on the server.
The production image itself contained a compiled `/app/sub2api` binary, not Go or
frontend source files.

## Reconstruction inputs

- Upstream Sub2API `v0.1.160`, commit
  `8bfbc5ca99bf2c0ac96e0f29ffd35eb6aca27e62`
- The deployment source-freeze bundle captured on 2026-07-17
- Frozen pricing and Model IQ patches used by the production build
- Production build manifest and runtime behavior used to define the feature set

## Restored feature set

- Model IQ backend, user interface, navigation, localization, and tests
- Model radar administration service, routes, interface, and navigation
- Group pricing, reference multipliers, and related channel/user interfaces
- Custom version comparison behavior
- Upstream `v0.1.160` image-input pricing fields

Unrelated historical customizations, including Cursor channel-specific changes,
were intentionally excluded because they were not part of the referenced
production release.

## Verification

- Frontend type checking and linting passed.
- Frontend test suite passed: 175 files and 1,209 tests.
- Wire dependency injection generation completed successfully.
- The affected backend packages compiled successfully on Linux.
- Targeted Go tests for Model IQ and model-radar configuration passed.

This tree is a functionally equivalent, development-ready reconstruction of the
referenced production feature set. It must not be described as byte-for-byte
identical to the unavailable `2c0dbd7f...` source commit.

## Current development baseline

- Upstream release: `v0.1.161`
- Upstream commit: `19149ca196eeae4a4482e5299dc6fa4ba0b06c8c`
- Replayed private feature commit: `baa4271`
- Development runtime version: `0.1.161+pricing-radar.model-iq.3`

The production reference above remains pinned to `v0.1.160` for exact rollback
and behavior comparison. New development builds use the `v0.1.161` baseline.
