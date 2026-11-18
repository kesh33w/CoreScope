# Recoverable bulk observation cleanup

Open **Customize → Cleanup**. An authenticated administrator can select an exact
observer ID, optional current IATA label, and timestamp range. Preview reports
matching observations, distinct packets and packets exclusively supported by
that selection. Confirming queues the whole selection, not individual rows.
The preview fixes an observation-ID and timestamp cutoff. A changed count requires
a new preview. Historic observations do not reliably contain their RF frequency:
this tool does **not** infer 910.525 MHz from an observer's current status.

The server remains read-only against measurement tables. The ingestor handles
requests in bounded 500-row transactions. It archives complete affected rows in
the same database before removing them. Shared packets and other observers remain;
packets with no remaining observations are removed. Archive/schema validation
fails closed. Failed jobs may have committed earlier batches; their archives
remain restorable. Keep the request ID shown in the dialog for status and restore.

Restore queues a writer operation and preserves newer observations already present.
The archive is retained indefinitely, uses disk space, and is not an off-machine
backup. Regular database backups are still appropriate.

**Restart every CoreScope server reading this database after cleanup or restore.**
Refreshing the browser alone cannot invalidate in-memory packet analytics. Readers
using SQLite WAL see transactionally consistent data while the ingestor processes
batches. Normal ingestion can continue between batches.

Node registries, cumulative statistics and shared advertisement-route evidence
remain historical unions: available attribution is insufficient for safe subtraction.
This is observation cleanup, not guaranteed erasure of every reference to a node.
Prevent recurrence separately with an observer blacklist or frequency-region filter.

Routes under `/api/admin/observation-cleanup` use the existing admin/API-key
requirement. No admin key means no public write access. Deployment does not delete
observations or enqueue work by itself.
