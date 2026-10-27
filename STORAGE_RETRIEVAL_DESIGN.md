# Storage and retrieval design (2026-10-28)

## What the current library says

The measured library has 3,073 unique JPEG originals occupying 17,570,988,391 bytes (16.36 GiB). Mean size is 5.72 MB. At the same mean, 18,000 images would occupy about 103 GB (95.9 GiB) of primary storage, and a second independent copy needs roughly the same again. The small and medium preview folders currently use about 25.6 MB and 304.5 MB. A stratified 32-file sample compressed with zlib level 3 had a median output/original ratio of 1.000 and a mean of 0.986. These are measurements of this library, not codec benchmarks or forecasts for every future phone.

Fotoro already names originals by SHA-256 and stores a matching file once. All 3,073 current hashes are unique, so exact-file deduplication cannot reduce this particular set further. Perceptual similarity may reveal burst shots, screenshots, or edited copies, but those are separate memories and must only be presented as review suggestions.

One pilot 2048 px display copy was generated from a 27.5 MB original and occupied 422 KB (1.5% of the source bytes). That image was selected by descending file size, so its ratio must not be projected to the whole library. It demonstrates the difference between a viewing copy and an archival original.

## How large photo stores approach this

Meta's [Haystack paper](https://www.usenix.org/conference/osdi10/finding-needle-haystack-facebooks-photo-storage) describes the cost of metadata I/O at billions of objects. Their system keeps a small lookup index, precomputes several display sizes, and serves hot photos through caches. Its packed-volume layout solves a much larger file-count problem than an 18,000-file home library; adopting that layout here would complicate repair and backup without addressing image transfer time.

[SQLite's query planner](https://www.sqlite.org/queryplanner.html) can walk an ordered multicolumn index for a timeline. Fotoro already has an expression index on `(COALESCE(captured_at, imported_at) DESC, id DESC)`. The remaining listing improvements are cursor pagination and avoiding repeated counts for later pages. Keeping the catalogue in SQLite and the immutable media in hash-addressed files is simpler than a data lake format. Columnar formats such as Parquet help analytical scans of metadata; they do not make one photo download faster.

[Restic's design](https://github.com/restic/restic/blob/master/doc/design.rst) and [Borg's documentation](https://borgbackup.readthedocs.io/en/stable/index.html) show how content-defined chunking saves space across *changed versions* of large files and backups. It is useful for a versioned document backup. Individual JPEGs are already compressed and rarely share byte-identical chunks, so chunking them adds indexing and read reconstruction work for little gain. A backup tool may still use chunking across backup snapshots.

For delivery, [responsive image guidance](https://web.dev/articles/serve-responsive-images) favors a file sized for the display instead of transferring the full original. Fotoro now has 256, 1024, and 2048 px JPEG tiers. The browser loads small tiles, then a 1024 px preview, then the 2048 px detail copy. It requests the unchanged original at deep zoom or explicit open/download. The browser decodes these JPEGs; the Go server serves precomputed bytes and supports HTTP byte ranges for originals. Generation is a bounded background job during ingestion, and `./fotoro backfill-detail 25` builds at most 25 older detail copies per run.

## Compression choices

| Technique | Likely value here | Decision |
| --- | --- | --- |
| ZIP/zlib/zstd around JPEG originals | The 32-file local zlib sample had nearly no median saving. | Do not wrap individual JPEG originals. |
| JPEG/WebP/AVIF display derivatives | Smaller transfer can improve browsing without changing originals. Google's [WebP study](https://developers.google.com/speed/webp/docs/webp_study) reports 25–34% smaller WebP than JPEG at matched SSIM on its datasets; this is not a measured Fotoro saving. | Benchmark derivative formats, decode speed, visual quality, and CPU before changing output format. Keep JPEG fallback. |
| Reversible JPEG → JPEG XL archival transcode | The [JPEG XL specification material](https://ds.jpeg.org/whitepapers/jpeg-xl-whitepaper.pdf) describes exact JPEG reconstruction for supported JPEG streams. This could save archival bytes, but requires a decoder and changes restore, range-serving, and verification paths. | Experimental only. Do not replace originals until a representative sample round-trips to identical SHA-256 and restore performance is measured. |
| Exact SHA-256 deduplication | Already implemented. | Keep it. Report logical versus physical bytes when multiple device records refer to one original. |
| Perceptual duplicate detection | Can suggest visually similar photos, but cannot assume they are interchangeable. | User-reviewed cleanup only; never automatic deletion. |
| Content-defined chunks | Strong for revised documents and versioned backups, weak for independent compressed photos. | Use in a backup layer if versioning warrants it; keep direct hash files for the hot library. |

## Next measured experiments

1. Benchmark 100 representative originals by format, size, and image type with reversible JPEG XL transcode and byte-identical restore. Record encoder time, decoder time, file size, metadata preservation, and failure rate. The current host has no `cjxl`/`djxl`, so no archival saving is claimed yet.
2. Compare 1024/2048 px JPEG, WebP, and AVIF derivatives at matched perceptual quality and measure browser decode time and memory on the target laptop and Android device. Do not use matching numerical “quality” settings as a quality comparison.
3. Record p50/p95 latency and transferred bytes for tile scroll, next-image navigation, first detail view, deep zoom, and cached revisit over LAN and Tailscale Funnel. Signed URLs now remain stable for one-hour windows while remaining valid for at most two hours, allowing private browser cache reuse during a session. Tune this policy using measured revisit patterns.
4. Add cursor pagination to the media API before collections reach hundreds of thousands of rows. The current indexed page/offset query is adequate at 18,000 entries but still scans earlier index entries on a deep page.

The product advantage to pursue is **exact, verifiable originals plus an adaptive delivery ladder**: the phone can safely offload bytes, the desktop can stay responsive, and a gallery visit costs only the resolution actually viewed. No lossy archive conversion should be silently applied to the user's only recoverable original.
