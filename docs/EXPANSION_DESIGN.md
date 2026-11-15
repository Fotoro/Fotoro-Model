# Fotoro v2 expansion design

Goal: turn Fotoro into the self-hosted archive people migrate to: dramatically
lower storage footprint at 100k+ images, near-instant access to cold data,
offline semantic search on modest hardware, capture-date-native organization,
and set-and-forget backup. Originals stay byte-exact and are never deleted.

## Tiered original storage

| Tier | On-disk form | Restore cost |
| --- | --- | --- |
| hot | `library/originals/<hh>/<sha256><ext>` raw bytes | 0 ms (direct range serve) |
| warm | `library/cold/files/<hh>/<sha256>.<jxl\|zst\|raw>` one lossless archive file per asset | one-file read + decode (~50-400 ms) |
| cold | member of `library/cold/batches/<yyyymm>-<nn>.fpack` | slice read + decode (~50-400 ms) |

Every compressed byte round-trips to the original SHA-256. Because the asset id
is already the SHA-256 of the original bytes, each cold/warm restore is verified
end-to-end for free: decoded bytes must hash to the asset id or the serve fails
and the archive member is marked bad. Original files are only removed after a
verified round-trip, and demotion requires a verified backup copy when a second
disk is configured.

### Codec choice

- `jxl`: JPEG XL *lossless JPEG transcode* (`JxlEncoderAddJPEGFrame` /
  `JxlDecoder` JPEG reconstruction, equivalent to `cjxl --lossless_jpeg=1`).
  JPEG XL is the only widely deployed codec that rewrites an existing JPEG
  codestream and guarantees bit-exact reconstruction of the original file,
  including all metadata. Expected saving ~15-25% on phone JPEGs (measured per
  batch and recorded). Implemented two ways, in priority order:
  1. embedded `internal/jxl` cgo binding to the installed `libjxl.so` — works
     out of the box where libjxl is present (Fedora package `libjxl`);
  2. external `cjxl`/`djxl` binaries when installed (`libjxl-utils`).
  When neither exists the codec is unavailable and zstd is used instead.
- `zstd`: pure-Go (`klauspost/compress`) framed blob, byte-exact restore. Used
  for non-JPEG originals (PNG, HEIC, video, documents) and as the universal
  fallback. Files that would grow (compressed formats already at entropy) are
  stored as `none` members instead so the format stays uniform.
- `none`: the member stores raw original bytes (still index-addressed). Chosen
  when compression saves < 1% — keeps the restore path identical.

The codec is recorded per asset (`archive_codec`); decode is dispatched by the
recorded codec, never by sniffing.

### Batching strategy

Warm files are grouped into monthly **packs** once they go quiet:

- Batch key = `YYYY-MM` of `COALESCE(captured_at, imported_at)` — calendar-native
  batching: a memory is served from at most a few packs, and packs align with
  the timeline UI and backup rotation.
- A pack is sealed when it reaches 2000 members or 256 MiB of compressed
  payload, whichever first — bounds member count, keeps index tiny (< 128 KiB),
  and makes a pack a reasonable unit for second-disk replication.
- Pack format (little-endian, append-only, no external tools needed):
  concatenated member bytes, then `[8B index_len][index JSON][8B index_len][8B
  magic "FTRPACK1"]`. A reader verifies the tail magic, reads the JSON index
  (`{hash: {offset, length, codec, raw_size}}`), then reads a member by seeking
  to its offset. The index is also mirrored into `cold_members` so locating a
  member is one indexed SQL lookup, not a file walk.
- Members hold the *already compressed* payload — moving warm→cold is append +
  index + delete, never recompression.
- Promotion out of a pack marks the member `retired`; `dead_bytes` accrues and
  packs compact in the background once > 40% dead (rewrite live members into a
  fresh pack, fsync, atomic swap, delete old file).

Why not single packed volumes for everything (Haystack-style): per-file cold
storage keeps repair, verification, and per-asset backup trivially auditable
and gives O(1) restore; packs are only for the long tail where inode count and
backup sync granularity matter at 100k+ assets.

### Promotion / demotion policy

- `hot` set target: `FOTORO_HOT_TARGET` (default 2000) most-valuable assets.
- Score = `access_count`, `last_accessed_at`, age; demotion picks lowest score
  first among assets past `FOTORO_HOT_GRACE_DAYS` (default 30) whose previews
  are ready and whose backup copy is verified (when backup configured).
- Reads bump `access_count`/`last_accessed_at`. A non-hot asset reaching
  `FOTORO_PROMOTE_AFTER` accesses (default 3) is promoted to `hot` on the next
  maintenance pass (and its bytes restored immediately for the current serve).
- Disk-pressure relief: when primary free space drops below
  `FOTORO_TIER_MIN_FREE` (default 8%), the worker demotes additional hot assets
  oldest-first until the reserve is restored.
- All transitions are per-asset mutex guarded, atomic (temp+fsync+rename), and
  resumable: a crash mid-demote leaves either the original or a verified
  archive, never a half state.

### Rehydration cache

Decoded originals land in `.cache/rehydrated/<hh>/<hash><ext>`; the content
handlers serve that file with normal range/conditional support. An LRU sweep
keeps the cache under `FOTORO_REHYDRATE_CACHE_MB` (default 8192). Display
copies (256/1024/2048) are never tiered — the grid and viewer stay instant;
only full-original fetches touch cold bytes.

## Capture-date-first organization

- `captured_at` is already extracted pre-commit on every ingest path. The new
  `fotoro fix-dates --prefer-exif` mode also repairs rows whose `captured_at`
  fell back to import time when EXIF now yields a better date, and filename
  inference covers metadata-free files (`IMG_YYYYMMDD_HHMMSS`,
  `PXL_`, `Screenshot_`, `WhatsApp`, `signal-YYYY-MM-DD`, `mmexport` ms-epoch).
- New calendar surface: `GET /api/v1/calendar` (year→month→day counts over
  `COALESCE(captured_at, imported_at)`), `GET /api/v1/calendar/day?date=`,
  `GET /api/v1/memories/on-this-day`. Calendar health = count of assets still
  missing `captured_at`, surfaced in stats and diagnostics.

## Auto-backup & reliability

- Artifact-aware backup: hot assets copy raw bytes (hash = asset id); warm
  assets copy the archive file (hash = `archive_sha256`); cold members ride
  pack-level copies (`cold_packs.pack_sha256`). `storage_copies` keeps one row
  per asset with the artifact path it depends on.
- Catalogue snapshots: daily `VACUUM INTO` of fotoro.db into
  `<backup>/catalogue/fotoro-<ts>.db` + SHA-256 sidecar, retaining the last 7.
  The runbook is explicit: a media copy alone is not a backup — restore needs
  catalogue + originals.
- Unsafe configs rejected at startup and per-op: same filesystem, unmounted
  target, backup inside primary root or vice versa, `/` as mount.
- Health: `/api/v1/health` aggregates primary free bytes, backup pending count
  + oldest pending age, queue depth, `PRAGMA quick_check`, rehydrated cache
  size, tier distribution.

## Local semantic search (embeddinggemma)

- Model: Google `embeddinggemma` 300M (768-d, Matryoshka). Ships as a GGUF and
  is served by the same `llama-server --embeddings` process already used for
  nomic (`EMBED_ADDR`, `/v1/embeddings`) — no new runtime. Default quant:
  `q4_0` (~160-300 MB RAM), ordered below the VLM in the memory guard.
- `FOTORO_EMBED_DIM` (default 256) truncates Matryoshka dimensions then
  renormalizes — 3x less index RAM and faster cosine at a small recall cost;
  set 768 to keep full precision.
- Index stays in memory but becomes incremental: vectors keyed by `images.id`,
  delta-loaded on `embedded_at`, `Index.Upsert` called at write time (no more
  30 s full reloads), and scoring parallelized across cores — 100k×256-d query
  lands in low tens of ms.
- `images` gains `embed_model`, `embed_dim`, `embedded_at`, enabling a clean
  `fotoro embed backfill` re-embed when the model changes.
- Derived data is never required to serve or preserve originals; embedding
  failures degrade search only.

## API additions

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/storage/tiers` | hot/warm/cold counts+bytes, savings, cache, packs |
| POST | `/api/v1/storage/tier/run` | bounded demote/promote pass (default 25 items) |
| GET | `/api/v1/media?cursor=` | keyset pagination on (capture,id) for 100k+ lists |
| GET | `/api/v1/calendar`, `/api/v1/calendar/day` | capture-date hierarchy |
| GET | `/api/v1/memories/on-this-day` | same month-day across years |
| GET | `/api/v1/embed/status` | model, dim, vectors, pending, server health |
| POST | `/api/v1/embed/backfill` | enqueue embedding backfill |
| GET | `/api/v1/health` | consolidated operator health |
| POST | `/api/v1/storage/catalogue-snapshot` | force DB snapshot to backup disk |

CLI: `fotoro storage` (tier report), `fotoro cold status|run|restore
<hash>|compact`, `fotoro backup status|snapshot`, `fotoro embed status|backfill`,
`fotoro calendar`, `fotoro fix-dates [--prefer-exif] [--infer-names]`.

## Lifecycle

ingest (verify sha256 → original file → captured_at → previews queued) →
hot serve → idle → demote to warm (compress+verify round-trip, original
removed) → months quiet → pack into cold batch → read hits → decode+verify →
rehydrated cache → promote on repeat access → backup mirrors every artifact
and the catalogue daily. Every step is bounded, interruptible, idempotent, and
recorded for the operator.
