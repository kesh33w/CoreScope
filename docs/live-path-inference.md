# Live path inference

The Live map uses `HopResolver.inferPath`, the same whole-path resolver used by packet details. Each observation supplies its own receiver and server-resolved path. Sender coordinates and the receiver’s coordinates or IATA region anchor the inference.

Unknown hops retain their positions in the path sequence but have no map coordinates. Animation, unreached TRACE lines, and clickable paths stop at each gap. The former interpolated-hop toggle is removed. Normal packet audio remains available; separated segments do not restart synchronized audio at hop zero.

Receiver context loads in bulk with nodes and refreshes on the existing one-minute refresh. Missing anchors fail closed, while failed refreshes retain the previous valid roster and region centers. No packet-specific network request or database change is added. No resolver model thresholds are changed.

## Validation

Run `sh test-all.sh`. `tests/unit/test-live-path-inference.js` covers distant prefix collisions, ordered gaps, repeated prefixes, origin validation, receiver refresh/failure, animation and clickable paths, split audio, and unreached TRACE segments. `tests/unit/test-live.js` covers existing Live behavior.

Before deployment, validate desktop and narrow-screen layout, live traffic, repeated navigation, pause/resume, and a representative burst against an isolated preview. Keep synthetic traffic out of the production ingest path. Preserve each instance’s existing backend image and customizations when deploying a frontend-only change; retain its prior image for rollback.
