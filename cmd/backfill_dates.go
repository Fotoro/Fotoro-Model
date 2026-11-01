package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/media"
	"fotoro/internal/validate"
)

type captureCandidate struct {
	id, path, kind string
	importedAt     time.Time
}

type datedAsset struct {
	id       string
	captured time.Time
}

// RunBackfillCaptureDates repairs only records with no capture timestamp. It
// never changes the original media files and can be rerun after interruption.
func RunBackfillCaptureDates(dbPath string, dryRun bool) error {
	database, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	rows, err := database.Query(`SELECT id, storage_path, media_kind, imported_at
		FROM media_assets WHERE captured_at IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	var candidates []captureCandidate
	for rows.Next() {
		var item captureCandidate
		if err := rows.Scan(&item.id, &item.path, &item.kind, &item.importedAt); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	var ready []datedAsset
	var metadataCount, modifiedCount int
	for _, item := range candidates {
		info, err := os.Stat(item.path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		captured := embeddedCaptureDate(item.path, item.kind)
		if !media.ValidCapturedAt(captured) {
			captured = time.Time{}
		}
		if captured.IsZero() {
			// A copied upload receives the import timestamp. Only a preserved
			// older file time is useful as a fallback for a legacy library.
			modified := info.ModTime()
			if media.ValidCapturedAt(modified) && modified.Before(item.importedAt.Add(-24*time.Hour)) {
				captured = modified
				modifiedCount++
			}
		} else {
			metadataCount++
		}
		if !captured.IsZero() {
			ready = append(ready, datedAsset{id: item.id, captured: captured.UTC()})
		}
	}
	if !dryRun {
		for start := 0; start < len(ready); start += 100 {
			end := min(start+100, len(ready))
			tx, err := database.Begin()
			if err != nil {
				return err
			}
			for _, item := range ready[start:end] {
				if _, err = tx.Exec(`UPDATE media_assets SET captured_at = ? WHERE id = ? AND captured_at IS NULL`, item.captured, item.id); err != nil {
					break
				}
				if _, err = tx.Exec(`UPDATE images SET taken_at = COALESCE(taken_at, ?) WHERE hash = ?`, item.captured, item.id); err != nil {
					break
				}
			}
			if err != nil {
				tx.Rollback()
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
		}
	}
	mode := "updated"
	if dryRun {
		mode = "would update"
	}
	fmt.Printf("Timeline dates: checked %d; %s %d (%d embedded, %d preserved file times); %d unresolved\n",
		len(candidates), mode, len(ready), metadataCount, modifiedCount, len(candidates)-len(ready))
	return nil
}

func embeddedCaptureDate(path, kind string) time.Time {
	if kind == "image" {
		return validate.ExtractDateTaken(path)
	}
	if kind != "video" {
		return time.Time{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries",
		"format_tags=creation_time:stream_tags=creation_time", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if captured, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(line)); err == nil &&
			media.ValidCapturedAt(captured) && captured.Year() != 1904 {
			return captured
		}
	}
	return time.Time{}
}
