package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/media"
	"fotoro/internal/ollama"
)

func openMediaService(dbPath string) (*media.Service, *db.DB, error) {
	database, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	svc, err := media.NewService(database, dbPath)
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	return svc, database, nil
}

func humanBytes(n int64) string {
	const u = 1024
	switch {
	case n >= u*u*u:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(u*u*u))
	case n >= u*u:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(u*u))
	case n >= u:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(u))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// RunStorage prints the tiered-storage report.
func RunStorage(dbPath string) {
	svc, _, err := openMediaService(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	stats, err := svc.TierStats()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "hot\t%d assets\t%s\n", stats.HotAssets, humanBytes(stats.HotBytes))
	fmt.Fprintf(w, "warm\t%d assets\t%s originals in %s archives\n",
		stats.WarmAssets, humanBytes(stats.WarmBytes), humanBytes(stats.ArchiveBytes))
	fmt.Fprintf(w, "cold\t%d assets\t%s originals in %d packs (%s)\n",
		stats.ColdAssets, humanBytes(stats.ColdBytes), stats.Packs, humanBytes(stats.PackBytes))
	fmt.Fprintf(w, "saved\t%s vs all-hot storage\n", humanBytes(stats.SavedBytes))
	fmt.Fprintf(w, "decode cache\t%s\n", humanBytes(stats.CacheBytes))
	fmt.Fprintf(w, "primary free\t%s of %s\n", humanBytes(stats.PrimaryFree), humanBytes(stats.PrimaryTotal))
	w.Flush()
}

// RunCold drives cold-storage operations:
//
//	fotoro cold status           - tier counts (same as fotoro storage)
//	fotoro cold run [limit]      - one demote/promote/pack pass
//	fotoro cold restore <hash>   - rehydrate a cold/warm asset to hot
//	fotoro cold compact          - rewrite packs with dead members
func RunCold(dbPath string, args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	svc, _, err := openMediaService(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch sub {
	case "status":
		stats, err := svc.TierStats()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("hot %d | warm %d | cold %d | packs %d | saved %s\n",
			stats.HotAssets, stats.WarmAssets, stats.ColdAssets, stats.Packs,
			humanBytes(stats.SavedBytes))
	case "run":
		limit := 25
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &limit)
		}
		res, err := svc.RunTierPass(limit)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		packed, err := svc.PackWarmAssets(100)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("demoted %d, promoted %d, packed %d",
			len(res.Demoted), len(res.Promoted), len(packed.Packed))
		for _, e := range append(res.Errors, packed.Errors...) {
			fmt.Printf("\n  ! %s", e)
		}
		fmt.Println()
	case "restore":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: fotoro cold restore <asset-hash>")
			os.Exit(1)
		}
		if err := svc.PromoteAsset(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("restored to hot storage")
	case "compact":
		n, err := svc.CompactPacks(4)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("compacted %d packs\n", n)
	default:
		fmt.Fprintln(os.Stderr, "Usage: fotoro cold {status|run [limit]|restore <hash>|compact}")
		os.Exit(2)
	}
}

// RunBackup reports copy health and manages catalogue snapshots:
//
//	fotoro backup status    - verified/pending/failed copy counts
//	fotoro backup snapshot  - VACUUM the catalogue to the backup disk
//	fotoro backup sync      - run one bounded copy pass now
func RunBackup(dbPath string, args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	svc, _, err := openMediaService(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch sub {
	case "status":
		st, err := svc.BackupStatus()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("configured %v | available %v\n", st.Configured, st.Available)
		if st.Error != "" {
			fmt.Printf("error: %s\n", st.Error)
		}
		fmt.Printf("verified %d | pending %d | failed %d | missing primary %d | corrupt %d\n",
			st.Verified, st.Pending, st.Failed, st.Missing, st.Corrupt)
		fmt.Println("remember: catalogue + originals are both required for recovery")
	case "snapshot":
		path, err := svc.SnapshotCatalogue(dbPath, 7)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("snapshot: %s\n", path)
	case "sync":
		fmt.Fprintln(os.Stderr, "sync runs in the background while the API is on; start it with ./fotoroctl start")
	default:
		fmt.Fprintln(os.Stderr, "Usage: fotoro backup {status|snapshot}")
		os.Exit(2)
	}
}

// RunEmbed drives semantic-search embedding ops:
//
//	fotoro embed status        - embedded/pending counts for the active model
//	fotoro embed run [limit]   - embed pending captions (default: all)
//	fotoro embed check         - verify the embed backend is reachable
func RunEmbed(dbPath string, args []string) {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	client := ollama.NewEmbedClient()
	if sub == "check" {
		if err := client.HealthCheck(); err != nil {
			fmt.Printf("embed backend unreachable: %v\nmodel: %s dim: %d\n", err, client.Model(), client.Dim())
			os.Exit(1)
		}
		fmt.Printf("ok: model=%s dim=%d\n", client.Model(), client.Dim())
		return
	}
	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer database.Close()

	switch sub {
	case "status":
		pending, err := EmbedPending(database.DB, client)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		var total, embedded int
		database.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN embedding IS NOT NULL THEN 1 ELSE 0 END),0) FROM images`).Scan(&total, &embedded)
		fmt.Printf("model %s dim %d | embedded %d/%d | pending %d\n",
			client.Model(), client.Dim(), embedded, total, pending)
		if err := client.HealthCheck(); err != nil {
			fmt.Printf("backend unreachable: %v\n", err)
		} else {
			fmt.Println("backend ok")
		}
	case "run":
		if err := client.HealthCheck(); err != nil {
			fmt.Fprintf(os.Stderr, "embed backend unreachable: %v\n", err)
			os.Exit(1)
		}
		limit := 1 << 30
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &limit)
		}
		start := time.Now()
		done, skipped := 0, 0
		for limit > 0 {
			batch := 200
			if batch > limit {
				batch = limit
			}
			d, s, err := BackfillEmbeddings(database.DB, client, batch)
			if err != nil {
				fmt.Fprintf(os.Stderr, "embed failed: %v\n", err)
				os.Exit(1)
			}
			done += d
			skipped += s
			if d+s == 0 || d+s < batch {
				break
			}
			limit -= batch
			fmt.Printf("%d embedded, %d skipped...\n", done, skipped)
		}
		fmt.Printf("embedded %d, skipped %d (no text) in %.1fs\n",
			done, skipped, time.Since(start).Seconds())
	default:
		fmt.Fprintln(os.Stderr, "Usage: fotoro embed {status|run [limit]|check}")
		os.Exit(2)
	}
}
