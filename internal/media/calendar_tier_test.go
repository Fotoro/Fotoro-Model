package media

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func testID(i int) string {
	return fmt.Sprintf("%064x", i)
}

// seedCalendarAsset inserts a media_assets row directly so tests control
// captured_at/imported_at precisely.
func seedCalendarAsset(t *testing.T, s *Service, id string, captured *time.Time, imported time.Time) {
	t.Helper()
	var capVal interface{}
	if captured != nil {
		capVal = *captured
	}
	if _, err := s.db.Exec(`INSERT INTO media_assets
		(id, hash, original_name, storage_path, media_kind, mime_type, file_size,
		 status, captured_at, imported_at, checksum_verified, preview_status)
		VALUES (?, ?, ?, ?, 'image', 'image/jpeg', 10, 'verified', ?, ?, 1, 'pending')`,
		id, id, "seed-"+id[:8]+".jpg", "/seed/"+id+".jpg", capVal, imported.UTC()); err != nil {
		t.Fatal(err)
	}
}

// TestListAssetsCursorPagination pages a seeded library with a small limit and
// asserts full coverage, no duplicates, and stable (capture desc, id desc)
// ordering - including a captured_at tie and an asset with no capture date.
func TestListAssetsCursorPagination(t *testing.T) {
	s := newTestService(t)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	type seed struct {
		id       string
		captured *time.Time
		imported time.Time
	}
	var seeds []seed
	add := func(id string, captured *time.Time, imported time.Time) {
		seeds = append(seeds, seed{id: id, captured: captured, imported: imported})
		seedCalendarAsset(t, s, id, captured, imported)
	}
	for i := 1; i <= 8; i++ {
		c := base.Add(time.Duration(i) * time.Hour)
		add(testID(i), &c, base)
	}
	tie := base.Add(5 * time.Hour) // ties with i==5's captured_at
	add(testID(100), &tie, base)
	add(testID(101), nil, base.Add(3*time.Hour)) // falls back to imported_at

	sort.Slice(seeds, func(i, j int) bool {
		ti, tj := seeds[i].imported, seeds[j].imported
		if seeds[i].captured != nil {
			ti = *seeds[i].captured
		}
		if seeds[j].captured != nil {
			tj = *seeds[j].captured
		}
		if ti.Equal(tj) {
			return seeds[i].id > seeds[j].id
		}
		return ti.After(tj)
	})

	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > len(seeds) {
			t.Fatal("pagination did not terminate")
		}
		page, next, err := s.ListAssetsCursor(cursor, 3, "")
		if err != nil {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
		for _, a := range page {
			got = append(got, a.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != len(seeds) {
		t.Fatalf("coverage: got %d assets, want %d", len(got), len(seeds))
	}
	seen := map[string]bool{}
	for i, id := range got {
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
		if id != seeds[i].id {
			t.Fatalf("order mismatch at %d: got %s want %s", i, id, seeds[i].id)
		}
	}
}

// TestCalendarAggregation checks year->month->day bucketing over
// COALESCE(captured_at, imported_at) plus the missing-capture-date count.
func TestCalendarAggregation(t *testing.T) {
	s := newTestService(t)
	imp := time.Date(2023, 3, 10, 12, 0, 0, 0, time.UTC)
	c1 := time.Date(2021, 5, 4, 10, 0, 0, 0, time.UTC)
	c2 := time.Date(2021, 5, 4, 18, 30, 0, 0, time.UTC)
	c3 := time.Date(2021, 7, 9, 8, 0, 0, 0, time.UTC)
	c4 := time.Date(2022, 1, 2, 0, 0, 0, 0, time.UTC)
	seedCalendarAsset(t, s, testID(1), &c1, imp)
	seedCalendarAsset(t, s, testID(2), &c2, imp)
	seedCalendarAsset(t, s, testID(3), &c3, imp)
	seedCalendarAsset(t, s, testID(4), &c4, imp)
	seedCalendarAsset(t, s, testID(5), nil, imp) // missing capture date

	years, missing, err := s.Calendar()
	if err != nil {
		t.Fatal(err)
	}
	if missing != 1 {
		t.Fatalf("missing=%d want 1", missing)
	}
	y2021 := years[2021]
	if y2021 == nil || y2021.Total != 3 {
		t.Fatalf("2021 node: %+v", y2021)
	}
	if y2021.Months[5] != 2 || y2021.Months[7] != 1 {
		t.Fatalf("2021 months: %+v", y2021.Months)
	}
	if y2021.Days["2021-05-04"] != 2 || y2021.Days["2021-07-09"] != 1 {
		t.Fatalf("2021 days: %+v", y2021.Days)
	}
	if years[2022] == nil || years[2022].Total != 1 {
		t.Fatalf("2022 node: %+v", years[2022])
	}
	// The uncaptured asset lands in the 2023-03-10 bucket via imported_at.
	if years[2023] == nil || years[2023].Days["2023-03-10"] != 1 {
		t.Fatalf("2023 fallback: %+v", years[2023])
	}
}

// TestListAssetsByDay returns exactly one day's assets newest-first, with
// imported_at filling in for assets missing capture dates.
func TestListAssetsByDay(t *testing.T) {
	s := newTestService(t)
	imp := time.Date(2022, 6, 20, 12, 0, 0, 0, time.UTC)
	morning := time.Date(2022, 6, 15, 8, 0, 0, 0, time.UTC)
	evening := time.Date(2022, 6, 15, 20, 0, 0, 0, time.UTC)
	other := time.Date(2022, 6, 16, 8, 0, 0, 0, time.UTC)
	sameDay := time.Date(2022, 6, 15, 12, 0, 0, 0, time.UTC)
	seedCalendarAsset(t, s, testID(1), &morning, imp)
	seedCalendarAsset(t, s, testID(2), &evening, imp)
	seedCalendarAsset(t, s, testID(3), &other, imp)
	seedCalendarAsset(t, s, testID(4), nil, sameDay)

	assets, err := s.ListAssetsByDay("2022-06-15", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 {
		t.Fatalf("day list: got %d assets", len(assets))
	}
	want := []string{testID(2), testID(4), testID(1)} // 20:00, 12:00, 08:00
	for i, id := range want {
		if assets[i].ID != id {
			t.Fatalf("order at %d: got %s want %s", i, assets[i].ID, id)
		}
	}
}

// TestOnThisDay returns earlier-year assets sharing today's month-day, capped
// per year, excluding the current year and non-matching days.
func TestOnThisDay(t *testing.T) {
	s := newTestService(t)
	now := time.Now().UTC()
	if now.Month() == time.February && now.Day() == 29 {
		t.Skip("month-day does not exist in every year")
	}
	imp := now
	prev1 := time.Date(now.Year()-1, now.Month(), now.Day(), 10, 0, 0, 0, time.UTC)
	prev2a := time.Date(now.Year()-2, now.Month(), now.Day(), 9, 0, 0, 0, time.UTC)
	prev2b := time.Date(now.Year()-2, now.Month(), now.Day(), 11, 0, 0, 0, time.UTC)
	otherMonth := time.Date(now.Year()-3, (now.Month()%12)+1, 15, 0, 0, 0, 0, time.UTC)
	seedCalendarAsset(t, s, testID(1), &prev1, imp)
	seedCalendarAsset(t, s, testID(2), &prev2a, imp)
	seedCalendarAsset(t, s, testID(3), &prev2b, imp)
	seedCalendarAsset(t, s, testID(4), &now, imp)        // same year: excluded
	seedCalendarAsset(t, s, testID(5), &otherMonth, imp) // different month-day

	assets, err := s.OnThisDay(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 2 || assets[0].ID != testID(1) || assets[1].ID != testID(3) {
		t.Fatalf("per_year=1: got %+v", assets)
	}
	assets, err = s.OnThisDay(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 || assets[0].ID != testID(1) ||
		assets[1].ID != testID(3) || assets[2].ID != testID(2) {
		t.Fatalf("per_year=3: got %+v", assets)
	}
}

// TestBackupBacklogAge verifies pending count and oldest-pending age against
// the same predicate BackupStatus uses.
func TestBackupBacklogAge(t *testing.T) {
	s := newTestService(t)
	old := time.Now().UTC().Add(-72 * time.Hour)
	recent := time.Now().UTC().Add(-time.Hour)
	seedCalendarAsset(t, s, testID(1), nil, old)
	seedCalendarAsset(t, s, testID(2), nil, recent)

	b, err := s.BackupBacklog()
	if err != nil {
		t.Fatal(err)
	}
	if b.Pending != 0 || b.OldestPendingSeconds != -1 {
		t.Fatalf("empty backlog: %+v", b)
	}
	for _, id := range []string{testID(1), testID(2)} {
		if _, err := s.db.Exec(`INSERT INTO storage_copies
			(media_asset_id, storage_name, storage_path, status, checksum_verified, verified_at)
			VALUES (?, 'primary', '/seed/x', 'verified', 1, ?)`, id, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	b, err = s.BackupBacklog()
	if err != nil {
		t.Fatal(err)
	}
	if b.Pending != 2 || b.OldestPendingSeconds < int64((71*time.Hour).Seconds()) {
		t.Fatalf("backlog: %+v", b)
	}
	if _, err := s.db.Exec(`INSERT INTO storage_copies
		(media_asset_id, storage_name, storage_path, status, checksum_verified, verified_at)
		VALUES (?, 'backup', '/backup/x', 'verified', 1, ?)`, testID(1), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	b, err = s.BackupBacklog()
	if err != nil {
		t.Fatal(err)
	}
	if b.Pending != 1 || b.OldestPendingSeconds < 3500 || b.OldestPendingSeconds > 3700 {
		t.Fatalf("after partial backup: %+v", b)
	}
}
