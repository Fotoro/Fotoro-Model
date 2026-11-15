# Tiered storage: hot / warm / cold

Fotoro keeps every original byte-exact while shrinking the on-disk footprint
of media nobody has looked at recently. Three tiers trade space for latency:

| Tier | Layout | Restore cost | Purpose |
| --- | --- | --- | --- |
| hot  | raw original at `library/originals/<hh>/<sha><ext>` | none | recent / frequently viewed |
| warm | per-asset archive `library/cold/files/<hh>/<sha>.<jxl|zst|raw>` | one file decode (~100 ms per MP of JPEG) | infrequently viewed |
| cold | member of a monthly pack `library/cold/batches/<yyyymm>-<nn>.fpack` | index lookup + member decode | long tail of the library |

Every archive decodes back to the identical source bytes. Demotion rewrites a
record only after the decoded payload hashes to the asset's SHA-256; the raw
original is deleted last. Restoration re-verifies the same hash before the
asset is served or promoted.

## Codecs

- **JPEG** - transcoded to JPEG XL lossless (`internal/jxl`, cgo binding to
  the system `libjxl.so`). The encoder feeds the original JPEG bitstream, not
  decoded pixels; the decoder emits the JBRD reconstruction box and recovers
  the source JPEG bit-exactly. Measured on real library photos: about 18%
  smaller (81.7% ratio). Encode effort 4 is the default - effort 9 costs ~18x
  CPU for under 0.1% more savings.
- **everything else** - zstd via `klauspost/compress` (`internal/cold`).
- **raw** - stored uncompressed when compression saves under 1%.

The codec is recorded in `media_assets.archive_codec`; decode dispatches on
the recorded codec, never on sniffing.

## Pack format

Warm archives are grouped into monthly packs keyed on
`COALESCE(captured_at, imported_at)` - the same date the timeline uses. A
pack seals at 2000 members or 256 MiB of compressed payload. Layout:

```
[member payloads concatenated][8-byte index len][JSON index][8-byte index len]["FTRPACK1"]
```

The trailer means the reader validates the magic and parses only the index;
members stream out by offset/length without decompressing the whole pack.
`cold_members` mirrors the index in SQL so member lookup never touches disk.

## Lifecycle

1. **Demote** (`RunTierPass`): assets past the hot-set target and not
   recently accessed are archived to warm. If a backup disk is configured,
   demotion requires the archive artifact to be backup-safe first - the raw
   original is never dropped before a verified second copy exists.
2. **Serve** (`ContentPathTiered`): a request for warm/cold content decodes
   into a bounded rehydrated cache (`library/cold/cache`) and serves from it.
   Access count and `last_accessed_at` are recorded.
3. **Pack**: idle warm archives move into the current month's pack; the warm
   file is removed after the pack entry is written and verified.
4. **Promote**: frequently accessed cold members are restored to hot
   originals and their pack member is retired.
5. **Compact**: packs with retired members are rewritten without the dead
   payloads when enough space is reclaimable.

All passes take explicit limits, are interruptible between assets, and
resume from SQL state - there is no journal to lose.

## Operators

- `fotoro storage` - tier counts, savings, decode-cache size, free space
- `fotoro cold status|run [limit]|restore <hash>|compact`
- `GET /api/v1/storage/tiers` - same numbers as JSON
- `POST /api/v1/storage/tier/run` - one bounded demote+pack pass

The maintenance worker runs a tier pass, pack pass and single-pack
compaction every 15 minutes while the API is awake.

## Failure boundaries

- corrupt archive/pack bytes: decode fails the hash check; the original is
  retained and the error is recorded in `storage_copies`/pack state
- pack trailer or index damaged: the pack is rejected (`ErrPackSealed`,
  index mismatch); members backed up elsewhere can still be re-imported
- `library/cold/cache` is disposable - delete it any time; missing entries
  are simply rehydrated again
- warm/cold assets are excluded from naive file listings; the catalogue
  remains the source of truth for what exists
