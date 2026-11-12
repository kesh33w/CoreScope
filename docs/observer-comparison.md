# Comparing observers and hardware changes

Open **Observers → Compare**. Pick receiver A and a reference receiver B on the same RF settings. Choose a 15-minute, 1-hour, 3-hour, 6-hour or 24-hour window. Leave the end time blank for the latest window.

The page separates stored receptions, distinct message hashes, additional recorded relay paths and distinct stored frames. It preserves alternate paths. Stored receptions are database records, not a hardware RX counter: ingestion may have collapsed identical reports.

Paired SNR matches the same stored wire bytes within 10 seconds, once per observation. Positive A-minus-B means A had higher SNR for those pairs. Missing reception-specific bytes are excluded, never replaced with the transmission's canonical bytes. Flood/direct filters also require a reception-specific header. A small sample or differing RF settings cannot establish antenna performance.

## Before and after

Enter a change time and select equal before/after windows. B is the reference for changing traffic. The displayed A receptions per 100 at B is a reception-count ratio, not a delivery percentage; it can exceed 100. Compare alongside the counts and paired SNR. Uptime, radio transmissions, MQTT gaps and propagation can still affect results.

Private experiment markers save a short note, change time and observer pair in this browser's local storage. They are not uploaded, embedded in URLs or shared with other browsers. They are visible to someone using the same browser profile. Clearing site data removes them. At most 50 markers are retained. Remove a marker with its Remove button.

## Bounds and missing data

Each on-demand request reads the newest 50,000 site observation candidates in the selected window through the existing timestamp index, returning at most 10,000 stored receptions per observer. A request has a 3-second deadline; at most two run concurrently. No full-history COUNT query, database write, background worker or packet copy is added.

When either cap is reached, the page marks the sample partial. Reception totals are lower bounds; overlap and exclusives may be misleading. Shorten the window. An uncapped result means the full stored sample, not proof that the observer stayed online or every report arrived.

The endpoint requires the v3 numeric-timestamp observation schema. Legacy schemas return an explicit unsupported response rather than comparing ambiguous date strings. Blacklisted observers are unavailable.

## Validation

- Go: `cd cmd/server && go test ./...`.
- Frontend: `node tests/unit/test-observer-experiments.js` and `sh test-all.sh`.
- Browser: `node tests/e2e/test-observer-experiments.js`, with Playwright installed. Optional `CHROMIUM_PATH` selects the browser; `COMPARISON_QA_DIR` selects screenshot output. Uses local synthetic data, never production.

Future customizer work can expose the fixed sample caps and pairing tolerance. Keep bounds in place; larger values need measured query and memory costs first.
