package media

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fotoro/internal/cold"
)

const (
	packMaxMembers = 2000
	packMaxBytes   = 256 << 20
)

// PackWarmAssets seals quiet warm assets into a monthly cold pack. It picks
// the oldest month first so packs stay calendar-native and closes when the
// pack hits the member or byte cap or `limit` assets were processed.
func (s *Service) PackWarmAssets(limit int) (*TierPassResult, error) {
	if limit < 1 {
		limit = 100
	}
	tierMu.Lock()
	defer tierMu.Unlock()
	var month string
	err := s.db.QueryRow(`SELECT substr(COALESCE(captured_at, imported_at), 1, 7)
		FROM media_assets WHERE tier = 'warm'
		ORDER BY 1 ASC LIMIT 1`).Scan(&month)
	if err == sql.ErrNoRows {
		return &TierPassResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, archive_path, archive_codec, archive_size, file_size
		FROM media_assets WHERE tier = 'warm'
		AND substr(COALESCE(captured_at, imported_at), 1, 7) = ?
		AND archive_path IS NOT NULL AND archive_path != ''
		ORDER BY id LIMIT ?`, month, packMaxMembers+1)
	if err != nil {
		return nil, err
	}
	type cand struct {
		id, path, codec string
		size, raw       int64
	}
	var cands []cand
	var payload int64
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.path, &c.codec, &c.size, &c.raw); err == nil {
			cands = append(cands, c)
			payload += c.size
		}
		if len(cands) >= packMaxMembers || payload >= packMaxBytes {
			break
		}
	}
	rows.Close()
	if len(cands) > limit {
		cands = cands[:limit]
	}
	if len(cands) == 0 {
		return &TierPassResult{}, nil
	}

	seq, err := s.nextPackSeq(month)
	if err != nil {
		return nil, err
	}
	packPath := filepath.Join(s.packDir(), fmt.Sprintf("%s-%02d.fpack", month, seq))
	w, err := cold.NewPackWriter(packPath)
	if err != nil {
		return nil, err
	}
	res := &TierPassResult{}
	var liveBytes int64
	var added []cand
	for _, c := range cands {
		_, hdrSize, err := cold.ReadFileHeader(c.path)
		if err != nil {
			res.Errors = append(res.Errors, "pack read "+c.id[:12]+": "+err.Error())
			continue
		}
		codec, blob, err := cold.ReadPayload(c.path)
		if err != nil {
			res.Errors = append(res.Errors, "pack read "+c.id[:12]+": "+err.Error())
			continue
		}
		if hdrSize != c.raw {
			res.Errors = append(res.Errors, "pack size mismatch "+c.id[:12])
			continue
		}
		if err := w.Add(c.id, codec, c.raw, blob); err != nil {
			res.Errors = append(res.Errors, "pack add "+c.id[:12]+": "+err.Error())
			continue
		}
		liveBytes += c.size
		added = append(added, c)
	}
	if len(added) == 0 {
		w.Abort()
		return res, nil
	}
	if err := w.Seal(); err != nil {
		w.Abort()
		return res, err
	}
	packHash, err := hashFile(packPath)
	if err != nil {
		os.Remove(packPath)
		return res, err
	}

	packID, err := randomHex(8)
	if err != nil {
		return res, err
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO cold_packs
		(id, month, storage_path, member_count, live_bytes, pack_sha256, status, sealed_at)
		VALUES (?, ?, ?, ?, ?, ?, 'sealed', ?)`,
		packID, month, packPath, len(added), liveBytes, packHash, now); err != nil {
		return res, err
	}
	r, err := cold.OpenPack(packPath)
	if err != nil {
		return res, err
	}
	for _, c := range added {
		m, ok := r.Member(c.id)
		if !ok {
			r.Close()
			return res, fmt.Errorf("member %s missing from sealed pack", c.id[:12])
		}
		if _, err := tx.Exec(`INSERT INTO cold_members
			(media_asset_id, pack_id, member_offset, member_length, codec, raw_size, state)
			VALUES (?, ?, ?, ?, ?, ?, 'live')`,
			c.id, packID, m.Offset, m.Length, m.Codec.String(), m.RawSize); err != nil {
			r.Close()
			return res, err
		}
		if _, err := tx.Exec(`UPDATE media_assets SET tier = 'cold', archive_path = NULL,
			archive_codec = NULL, archive_size = 0, archive_sha256 = NULL,
			updated_at = ? WHERE id = ? AND tier = 'warm'`, now, c.id); err != nil {
			r.Close()
			return res, err
		}
	}
	r.Close()
	if err := tx.Commit(); err != nil {
		return res, err
	}
	for _, c := range added {
		_ = os.Remove(c.path)
		res.Packed = append(res.Packed, c.id)
	}
	return res, nil
}

func (s *Service) nextPackSeq(month string) (int, error) {
	var maxSeq sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(CAST(substr(storage_path, -7, 2) AS INTEGER))
		FROM cold_packs WHERE month = ?`, month).Scan(&maxSeq)
	if err != nil {
		return 1, nil
	}
	return int(maxSeq.Int64) + 1, nil
}

// CompactPacks rewrites packs whose dead bytes exceed the threshold into fresh
// packs, freeing space held by retired (promoted) members.
func (s *Service) CompactPacks(limit int) (int, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := s.db.Query(`SELECT id, storage_path, live_bytes, dead_bytes
		FROM cold_packs WHERE status = 'sealed' AND dead_bytes > 0
		AND dead_bytes * 100 > (live_bytes + dead_bytes) * 40 LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	type pack struct {
		id, path      string
		live, dead    int64
	}
	var packs []pack
	for rows.Next() {
		var p pack
		if err := rows.Scan(&p.id, &p.path, &p.live, &p.dead); err == nil {
			packs = append(packs, p)
		}
	}
	rows.Close()
	compacted := 0
	for _, p := range packs {
		if err := s.compactPack(p.id, p.path); err != nil {
			return compacted, err
		}
		compacted++
	}
	return compacted, nil
}

func (s *Service) compactPack(packID, oldPath string) error {
	tierMu.Lock()
	defer tierMu.Unlock()
	r, err := cold.OpenPack(oldPath)
	if err != nil {
		return err
	}
	newPath := oldPath + ".compact"
	w, err := cold.NewPackWriter(newPath)
	if err != nil {
		r.Close()
		return err
	}
	type memberRow struct {
		id    string
		codec string
	}
	var live []memberRow
	rows, err := s.db.Query(`SELECT media_asset_id, codec FROM cold_members
		WHERE pack_id = ? AND state = 'live'`, packID)
	if err != nil {
		r.Close()
		w.Abort()
		return err
	}
	for rows.Next() {
		var m memberRow
		if err := rows.Scan(&m.id, &m.codec); err == nil {
			live = append(live, m)
		}
	}
	rows.Close()
	var liveBytes int64
	offsets := map[string][2]int64{}
	for _, m := range live {
		blob, rawSize, codec, err := r.ReadMemberRaw(m.id)
		if err != nil {
			r.Close()
			w.Abort()
			return err
		}
		off := w.Offset()
		if err := w.Add(m.id, codec, rawSize, blob); err != nil {
			r.Close()
			w.Abort()
			return err
		}
		offsets[m.id] = [2]int64{off, int64(len(blob))}
		liveBytes += int64(len(blob))
	}
	r.Close()
	if err := w.Seal(); err != nil {
		w.Abort()
		return err
	}
	packHash, err := hashFile(newPath)
	if err != nil {
		os.Remove(newPath)
		return err
	}
	finalPath := oldPath
	if err := os.Rename(newPath, finalPath); err != nil {
		os.Remove(newPath)
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, off := range offsets {
		if _, err := tx.Exec(`UPDATE cold_members SET member_offset = ?, member_length = ?
			WHERE media_asset_id = ? AND pack_id = ?`, off[0], off[1], id, packID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE cold_packs SET live_bytes = ?, dead_bytes = 0,
		pack_sha256 = ?, member_count = ?, backup_path = NULL, backed_up_at = NULL
		WHERE id = ?`, liveBytes, packHash, len(live), packID); err != nil {
		return err
	}
	return tx.Commit()
}
