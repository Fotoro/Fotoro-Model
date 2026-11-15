package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"fotoro/internal/db"
	"fotoro/internal/media"
	"fotoro/internal/validate"
)

type captureCandidate struct {
	id, path, kind, name string
	importedAt           time.Time
	capturedAt           *time.Time
}

type datedAsset struct {
	id       string
	captured time.Time
}

// RunBackfillCaptureDates repairs records with no capture timestamp. With
// preferEXIF it also repairs rows whose stored capture date disagrees with
// embedded metadata; inferNames additionally derives dates from common
// camera/messenger filename patterns. Original files are never modified.
func RunBackfillCaptureDates(dbPath string, dryRun, preferEXIF, inferNames bool) error {
	database, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer database.Close()
	query := `SELECT id, storage_path, media_kind, imported_at, original_name, captured_at
		FROM media_assets WHERE captured_at IS NULL ORDER BY id`
	if preferEXIF {
		query = `SELECT id, storage_path, media_kind, imported_at, original_name, captured_at
			FROM media_assets ORDER BY id`
	}
	rows, err := database.Query(query)
	if err != nil {
		return err
	}
	var candidates []captureCandidate
	for rows.Next() {
		var item captureCandidate
		var captured sql.NullTime
		if err := rows.Scan(&item.id, &item.path, &item.kind, &item.importedAt,
			&item.name, &captured); err != nil {
			rows.Close()
			return err
		}
		if captured.Valid {
			t := captured.Time.UTC()
			item.capturedAt = &t
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	var ready []datedAsset
	var metadataCount, modifiedCount, nameCount int
	for _, item := range candidates {
		var captured time.Time
		if info, err := os.Stat(item.path); err == nil && info.Mode().IsRegular() {
			captured = embeddedCaptureDate(item.path, item.kind)
			if !media.ValidCapturedAt(captured) {
				captured = time.Time{}
			}
			if !captured.IsZero() {
				metadataCount++
			} else {
				modified := info.ModTime()
				if media.ValidCapturedAt(modified) && modified.Before(item.importedAt.Add(-24*time.Hour)) {
					captured = modified
					modifiedCount++
				}
			}
		}
		if captured.IsZero() && inferNames {
			if inferred := inferDateFromName(item.name); media.ValidCapturedAt(inferred) {
				captured = inferred
				nameCount++
			}
		}
		if captured.IsZero() {
			continue
		}
		if item.capturedAt != nil && !preferEXIF {
			continue
		}
		if item.capturedAt != nil && preferEXIF {
			// Skip when the stored date already matches metadata within a
			// minute; timezone-free EXIF parsing can drift by seconds.
			if diff := item.capturedAt.Sub(captured.UTC()); diff < time.Minute && diff > -time.Minute {
				continue
			}
		}
		ready = append(ready, datedAsset{id: item.id, captured: captured.UTC()})
	}
	if !dryRun {
		for start := 0; start < len(ready); start += 100 {
			end := min(start+100, len(ready))
			tx, err := database.Begin()
			if err != nil {
				return err
			}
			for _, item := range ready[start:end] {
				if preferEXIF {
					if _, err = tx.Exec(`UPDATE media_assets SET captured_at = ? WHERE id = ?`, item.captured, item.id); err != nil {
						break
					}
					if _, err = tx.Exec(`UPDATE images SET taken_at = ? WHERE hash = ?`, item.captured, item.id); err != nil {
						break
					}
					continue
				}
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
	fmt.Printf("Timeline dates: checked %d; %s %d (%d embedded, %d preserved file times, %d filenames); %d unresolved\n",
		len(candidates), mode, len(ready), metadataCount, modifiedCount, nameCount, len(candidates)-len(ready))
	return nil
}

// inferDateFromName derives a capture date from common camera and messenger
// filename conventions when embedded metadata is absent.
var dateNamePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:IMG|VID|PXL|MVIMG|BURST|SAVE)_(\d{4})(\d{2})(\d{2})_(\d{2})(\d{2})(\d{2})`),
	regexp.MustCompile(`Screenshot_(\d{4})-(\d{2})-(\d{2})-(\d{2})-(\d{2})-(\d{2})`),
	regexp.MustCompile(`signal-(\d{4})-(\d{2})-(\d{2})-(\d{2})(\d{2})(\d{2})`),
	regexp.MustCompile(`WhatsApp (?:Image|Video|Audio) (\d{4})-(\d{2})-(\d{2}) at (\d{2})\.(\d{2})\.(\d{2})`),
	regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})[T_ ](\d{2})[.:_-]?(\d{2})[.:_-]?(\d{2})`),
}
var reEpochMillis = regexp.MustCompile(`mmexport(\d{13})`)

func inferDateFromName(name string) time.Time {
	base := filepath.Base(name)
	for _, re := range dateNamePatterns {
		m := re.FindStringSubmatch(base)
		if m == nil {
			continue
		}
		parts := make([]int, 6)
		ok := true
		for i := 0; i < 6; i++ {
			v, err := strconv.Atoi(m[i+1])
			if err != nil {
				ok = false
				break
			}
			parts[i] = v
		}
		if !ok || parts[1] < 1 || parts[1] > 12 || parts[2] < 1 || parts[2] > 31 ||
			parts[3] > 23 || parts[4] > 59 || parts[5] > 60 {
			continue
		}
		t := time.Date(parts[0], time.Month(parts[1]), parts[2],
			parts[3], parts[4], parts[5], 0, time.UTC)
		if media.ValidCapturedAt(t) {
			return t
		}
	}
	if m := reEpochMillis.FindStringSubmatch(base); m != nil {
		if ms, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			t := time.UnixMilli(ms).UTC()
			if media.ValidCapturedAt(t) {
				return t
			}
		}
	}
	return time.Time{}
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
