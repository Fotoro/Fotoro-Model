# Recovery runbook

Restoring a Fotoro library requires **two** things: the SQLite catalogue and
the media artifacts. A second copy of media files alone is not a backup -
without the catalogue there are no capture dates, album structure, tier
metadata, upload state or device pairings.

## What to restore

| Component | Location | Contents |
| --- | --- | --- |
| catalogue | `fotoro.db` (+ `-wal`) or `catalogue-*.db` snapshot | all metadata |
| originals | `library/originals/` | hot-tier raw media |
| warm archives | `library/cold/files/` | per-asset compressed originals |
| packs | `library/cold/batches/` | cold-tier packed originals |
| endpoint key | printed by `fotoro endpoint-key` | decrypts the stored route |

Generated previews under `.cache/` and `library/cold/cache/` are disposable
and are regenerated from originals.

## Snapshot format

`fotoro backup snapshot` (or `POST /api/v1/storage/catalogue-snapshot`, or
the daily maintenance pass) writes `catalogue-<timestamp>.db` to the backup
disk via `VACUUM INTO`, keeping the last seven. A snapshot is a complete,
self-contained copy of the live database - safe to restore directly.

## Restore procedure

1. Install the host, create an empty data directory.
2. Copy the newest `catalogue-*.db` into place as `fotoro.db`.
3. Restore `library/originals/`, `library/cold/files/` and
   `library/cold/batches/` from the backup disk, preserving layout.
4. Start the API and run `fotoro storage`: tier counts should match the
   pre-loss report.
5. Run a verification pass (`POST /api/v1/storage/verify`) to SHA-256 check
   restored artifacts; any member that fails is reported, not silently
   skipped.
6. Re-run `fotoro nodesync` and re-enter the endpoint key on each device.

## Partial-loss modes

- **catalogue lost, media intact**: re-import the media tree (`fotoro
  ingest`); hashes deduplicate against themselves. Dates, captions, tiers
  and pairings are gone and must be rebuilt - this is why snapshots exist.
- **media lost, catalogue intact**: records persist but content requests
  fail; restore the matching artifacts from backup. Missing members are
  visible via `fotoro backup status` (missing/corrupt counts).
- **single corrupt archive**: decode failure is detected by hash check.
  Restore that member's backup copy, then `fotoro cold restore <hash>`.

## Safety invariants to rely on

- originals are never modified after commit; corruption is always a copy or
  disk problem, never an encode problem (every demotion proves a bit-exact
  round trip before the raw original is dropped)
- demotion while a backup disk is configured requires a verified copy of the
  archive artifact first
- the server refuses backup targets that share a filesystem with primary or
  are not a mounted volume
