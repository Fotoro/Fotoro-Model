package media

import (
	"fmt"
	"time"
)

// CalendarNode aggregates capture-date counts for the calendar surface.
type CalendarNode struct {
	Year   int            `json:"year"`
	Months map[int]int    `json:"months"`
	Days   map[string]int `json:"days"`
	Total  int            `json:"total"`
}

// Calendar returns year→month→day counts over capture time (falling back to
// import time), plus the count of assets still missing a capture date — the
// "calendar health" signal for fix-dates.
func (s *Service) Calendar() (map[int]*CalendarNode, int, error) {
	rows, err := s.db.Query(`SELECT substr(COALESCE(captured_at, imported_at), 1, 10) AS day,
		COUNT(*) FROM media_assets GROUP BY day`)
	if err != nil {
		return nil, 0, err
	}
	years := map[int]*CalendarNode{}
	for rows.Next() {
		var day string
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if len(day) != 10 {
			continue
		}
		var y, m, d int
		if _, err := fmt.Sscanf(day, "%d-%d-%d", &y, &m, &d); err != nil {
			continue
		}
		node := years[y]
		if node == nil {
			node = &CalendarNode{Year: y, Months: map[int]int{}, Days: map[string]int{}}
			years[y] = node
		}
		node.Months[m] += count
		node.Days[day] += count
		node.Total += count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	var missing int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM media_assets WHERE captured_at IS NULL`).Scan(&missing); err != nil {
		return nil, 0, err
	}
	return years, missing, nil
}

// ListAssetsByDay returns one calendar day's assets newest-first.
func (s *Service) ListAssetsByDay(day string, limit int) ([]Asset, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(assetSelect+`
		WHERE substr(COALESCE(captured_at, imported_at), 1, 10) = ?
		ORDER BY COALESCE(captured_at, imported_at) DESC, id DESC LIMIT ?`, day, limit)
	if err != nil {
		return nil, err
	}
	var assets []Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		assets = append(assets, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := s.decorateAssetPage(assets); err != nil {
		return nil, err
	}
	return assets, nil
}

// OnThisDay returns up to `perYear` assets sharing today's month-day in each
// earlier year — the "memories" surface.
func (s *Service) OnThisDay(perYear int) ([]Asset, error) {
	if perYear < 1 || perYear > 10 {
		perYear = 3
	}
	mmdd := time.Now().UTC().Format("01-02")
	rows, err := s.db.Query(assetSelect+`
		WHERE substr(COALESCE(captured_at, imported_at), 6, 5) = ?
		AND substr(COALESCE(captured_at, imported_at), 1, 4) != substr(?, 1, 4)
		ORDER BY COALESCE(captured_at, imported_at) DESC`, mmdd, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	var assets []Asset
	seen := map[string]int{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		day := ""
		if a.CapturedAt != nil {
			day = a.CapturedAt.UTC().Format("2006-01-02")
		} else {
			day = a.ImportedAt.UTC().Format("2006-01-02")
		}
		year := day[:4]
		if seen[year] >= perYear {
			continue
		}
		seen[year]++
		assets = append(assets, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := s.decorateAssetPage(assets); err != nil {
		return nil, err
	}
	return assets, nil
}

// ListAssetsCursor is keyset pagination over (capture time, id) DESC — stable
// and O(limit) at any depth, so a 100k+ library pages without OFFSET scans.
// Cursor format: "<unixnano>|<id>" of the previous page's last row; the time
// is bound as time.Time so it compares in the driver's stored format.
func (s *Service) ListAssetsCursor(cursor string, limit int, kind string) ([]Asset, string, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	where := ""
	args := make([]interface{}, 0, 4)
	if kind != "" {
		where = " WHERE media_kind = ?"
		args = append(args, kind)
	}
	if cursor != "" {
		var nanos int64
		var keyID string
		if n, err := fmt.Sscanf(cursor, "%d|%64s", &nanos, &keyID); err != nil || n != 2 {
			return nil, "", fmt.Errorf("bad cursor")
		}
		keyTime := time.Unix(0, nanos).UTC()
		cond := `(COALESCE(captured_at, imported_at) < ? OR
			(COALESCE(captured_at, imported_at) = ? AND id < ?))`
		if where == "" {
			where = " WHERE " + cond
		} else {
			where += " AND " + cond
		}
		args = append(args, keyTime, keyTime, keyID)
	}
	rows, err := s.db.Query(assetSelect+where+`
		ORDER BY COALESCE(captured_at, imported_at) DESC, id DESC LIMIT ?`,
		append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	var assets []Asset
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			rows.Close()
			return nil, "", err
		}
		assets = append(assets, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(assets) > limit {
		assets = assets[:limit]
	}
	if len(assets) == limit && limit > 0 {
		last := assets[len(assets)-1]
		ts := last.ImportedAt
		if last.CapturedAt != nil {
			ts = *last.CapturedAt
		}
		next = fmt.Sprintf("%d|%s", ts.UTC().UnixNano(), last.ID)
	}
	if err := s.decorateAssetPage(assets); err != nil {
		return nil, "", err
	}
	return assets, next, nil
}
