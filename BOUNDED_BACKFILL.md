# Bounded legacy ADVERT backfill

The legacy `from_pubkey` backfill previously ran an upfront COUNT through the
single SQLite writer connection. Even when there were no pending ADVERTs,
SQLite could scan a large NULL-key index and hold that connection for minutes
under storage contention. Starting this work in a goroutine did not isolate it
from MQTT startup or packet writes.

The backfill now snapshots the highest transmission ID, visits at most 256
rows per primary-key page, and releases the connection between every page,
including pages without matching ADVERTs. It updates only still-unfilled ADVERT
rows and preserves the existing empty-string sentinel. New rows beyond the
snapshot are left to normal ingest or a later backfill. No schema, feed,
retention, decryption, or packet-routing settings change.

Progress reports the discovered total so far; it becomes exact on completion.
This avoids an expensive preliminary count. The existing production caller
passes no progress callback.

Verification includes query-plan and row-bound assertions, writer availability
between pages, existing-key/non-ADVERT preservation, malformed-data sentinels,
exact extracted keys, sparse IDs, empty databases, and snapshot boundaries.
The in-memory 100,000-row no-match benchmark took 56–74 ms locally. This is not
a NAS latency guarantee: bounded batch size limits work between opportunities
for packet ingestion, but cannot prevent individual slow I/O operations.
