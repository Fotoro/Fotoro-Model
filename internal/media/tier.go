package media

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"fotoro/internal/cold"
)

// Tier names stored in media_assets.tier.
const (
	TierHot  = "hot"
	TierWarm = "warm"
	TierCold = "cold"
)

type tierConfig struct {
	hotTarget        int
	hotGraceDays     int
	promoteAfter     int64
	rehydrateCacheMB int64
	minFreePercent   int64
}

func tierConfigFromEnv() tierConfig {
	c := tierConfig{
		hotTarget:        2000,
		hotGraceDays:     30,
		promoteAfter:     3,
		rehydrateCacheMB: 8192,
		minFreePercent:   8,
	}
	if v, err := strconv.Atoi(os.Getenv("FOTORO_HOT_TARGET")); err == nil && v > 0 {
		c.hotTarget = v
	}
	if v, err := strconv.Atoi(os.Getenv("FOTORO_HOT_GRACE_DAYS")); err == nil && v >= 0 {
		c.hotGraceDays = v
	}
	if v, err := strconv.ParseInt(os.Getenv("FOTORO_PROMOTE_AFTER"), 10, 64); err == nil && v > 0 {
		c.promoteAfter = v
	}
	if v, err := strconv.ParseInt(os.Getenv("FOTORO_REHYDRATE_CACHE_MB"), 10, 64); err == nil && v >= 0 {
		c.rehydrateCacheMB = v
	}
	if v, err := strconv.ParseInt(os.Getenv("FOTORO_TIER_MIN_FREE"), 10, 64); err == nil && v > 0 && v < 90 {
		c.minFreePercent = v
	}
	return c
}

func (s *Service) warmArchivePath(hash string) string {
	return filepath.Join(s.storageRoot, "cold", "files", hash[:2], hash+".farc")
}

func (s *Service) packDir() string {
	return filepath.Join(s.storageRoot, "cold", "batches")
}

func (s *Service) rehydratedDir() string {
	return filepath.Join(s.cacheDir, "rehydrated")
}

func (s *Service) rehydratedPath(hash, name string) string {
	return filepath.Join(s.rehydratedDir(), hash[:2], hash+safeExtension(name))
}

var tierMu sync.Mutex // serializes tier transitions; cheap since passes are bounded

// TrackAccess records a full-content serve and returns the asset's tier.
// Thumbnail serves do not touch it — previews are never tiered.
func (s *Service) TrackAccess(id string) {
	now := time.Now().UTC()
	_, _ = s.db.Exec(`UPDATE media_assets SET access_count = access_count + 1,
		last_accessed_at = ? WHERE id = ? OR hash = ?`, now, id, id)
}

// ContentPathTiered resolves an asset to a servable file, rehydrating cold or
// warm bytes into the decode cache when needed. Callers serve the returned
// path read-only; it may be evicted later, so they must not hold it long-term.
func (s *Service) ContentPathTiered(id string) (string, string, error) {
	var path, mimeType, tier, archive, codecName, origName string
	var packPath sql.NullString
	err := s.db.QueryRow(`SELECT m.storage_path, m.mime_type, m.tier,
		COALESCE(m.archive_path, ''), COALESCE(m.archive_codec, ''), m.original_name,
		(SELECT p.storage_path FROM cold_members cm JOIN cold_packs p ON p.id = cm.pack_id
			WHERE cm.media_asset_id = m.id AND cm.state = 'live')
		FROM media_assets m WHERE m.id = ? OR m.hash = ? LIMIT 1`,
		id, id).Scan(&path, &mimeType, &tier, &archive, &codecName, &origName, &packPath)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	var assetID string
	_ = s.db.QueryRow(`SELECT id FROM media_assets WHERE id = ? OR hash = ?`, id, id).Scan(&assetID)
	if assetID == "" {
		assetID = id
	}
	switch tier {
	case TierWarm:
		return s.rehydrateWarm(assetID, archive, mimeType, origName)
	case TierCold:
		return s.rehydrateCold(assetID, packPath.String, mimeType, origName)
	default:
		return path, mimeType, nil
	}
}

func (s *Service) rehydrateWarm(assetID, archivePath, mimeType, origName string) (string, string, error) {
	raw, err := cold.ReadFile(archivePath)
	if err != nil {
		return "", "", fmt.Errorf("warm restore: %w", err)
	}
	return s.commitRehydrated(assetID, raw, mimeType, origName)
}

func (s *Service) rehydrateCold(assetID, packPath, mimeType, origName string) (string, string, error) {
	r, err := cold.OpenPack(packPath)
	if err != nil {
		return "", "", fmt.Errorf("cold restore: %w", err)
	}
	defer r.Close()
	raw, err := r.ReadMember(assetID)
	if err != nil {
		return "", "", fmt.Errorf("cold restore: %w", err)
	}
	return s.commitRehydrated(assetID, raw, mimeType, origName)
}

// commitRehydrated verifies decoded bytes against the asset id (which is the
// original's SHA-256), then places them in the LRU decode cache.
func (s *Service) commitRehydrated(assetID string, raw []byte, mimeType, origName string) (string, string, error) {
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != assetID {
		return "", "", fmt.Errorf("restored bytes failed hash verification for %s", assetID[:12])
	}
	dest := s.rehydratedPath(assetID, origName)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", "", err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	return dest, mimeType, nil
}

// DemoteAsset moves one hot original into a warm single-file archive after a
// verified round-trip. Safe to call concurrently; transitions are serialized.
func (s *Service) DemoteAsset(id string) error {
	tierMu.Lock()
	defer tierMu.Unlock()
	var hash, origName, path, tier string
	var backupVerified int
	err := s.db.QueryRow(`SELECT m.id, m.original_name, m.storage_path, m.tier,
		(SELECT COUNT(*) FROM storage_copies sc WHERE sc.media_asset_id = m.id
			AND sc.storage_name = 'backup' AND sc.status = 'verified' AND sc.checksum_verified = 1)
		FROM media_assets m WHERE m.id = ? OR m.hash = ? LIMIT 1`,
		id, id).Scan(&hash, &origName, &path, &tier, &backupVerified)
	if err != nil {
		return err
	}
	if tier != TierHot {
		return nil
	}
	if s.backupRoot != "" && backupVerified == 0 {
		return fmt.Errorf("refusing to demote %s: no verified backup copy", hash[:12])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	blob, codec, err := cold.Encode(cold.Choose(origName), raw)
	if err != nil {
		return fmt.Errorf("compress %s: %w", hash[:12], err)
	}
	back, err := cold.Decode(codec, blob)
	if err != nil {
		return fmt.Errorf("verify %s: %w", hash[:12], err)
	}
	sum := sha256.Sum256(back)
	if hex.EncodeToString(sum[:]) != hash {
		return fmt.Errorf("round-trip verification failed for %s", hash[:12])
	}
	archivePath := s.warmArchivePath(hash)
	if err := cold.WriteFile(archivePath, codec, int64(len(raw)), blob); err != nil {
		return err
	}
	archiveHash, err := hashFile(archivePath)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE media_assets SET tier = ?, archive_path = ?,
		archive_codec = ?, archive_size = ?, archive_sha256 = ?, archived_at = ?,
		access_count = 0, updated_at = ? WHERE id = ? AND tier = 'hot'`,
		TierWarm, archivePath, codec.String(), int64(len(blob)),
		archiveHash, now, now, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE storage_copies SET storage_path = ?, verified_at = ?
		WHERE media_asset_id = ? AND storage_name = 'primary'`, archivePath, now, hash); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("archive committed but original removal failed: %w", err)
	}
	return nil
}

// PromoteAsset restores a warm or cold asset to a hot raw original. The warm
// file is removed; cold members are retired so the pack can compact later.
func (s *Service) PromoteAsset(id string) error {
	tierMu.Lock()
	defer tierMu.Unlock()
	var hash, origName, tier, mime string
	var archive sql.NullString
	var packPath sql.NullString
	err := s.db.QueryRow(`SELECT m.id, m.original_name, m.tier, m.mime_type,
		m.archive_path,
		(SELECT p.storage_path FROM cold_members cm JOIN cold_packs p ON p.id = cm.pack_id
			WHERE cm.media_asset_id = m.id AND cm.state = 'live')
		FROM media_assets m WHERE m.id = ? OR m.hash = ? LIMIT 1`,
		id, id).Scan(&hash, &origName, &tier, &mime, &archive, &packPath)
	if err != nil {
		return err
	}
	if tier == TierHot {
		return nil
	}
	var raw []byte
	switch tier {
	case TierWarm:
		raw, err = cold.ReadFile(archive.String)
	case TierCold:
		var r *cold.PackReader
		r, err = cold.OpenPack(packPath.String)
		if err == nil {
			raw, err = r.ReadMember(hash)
			r.Close()
		}
	default:
		return fmt.Errorf("unknown tier %q", tier)
	}
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != hash {
		return fmt.Errorf("restored bytes failed hash verification for %s", hash[:12])
	}
	dest := s.assetStoragePath(hash, origName)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE media_assets SET tier = 'hot', archive_path = NULL,
		archive_codec = NULL, archive_size = 0, archive_sha256 = NULL, archived_at = NULL,
		access_count = 0, updated_at = ? WHERE id = ?`, now, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE storage_copies SET storage_path = ?, verified_at = ?
		WHERE media_asset_id = ? AND storage_name = 'primary'`, dest, now, hash); err != nil {
		return err
	}
	var memberLen int64
	var packID string
	if err := tx.QueryRow(`SELECT pack_id, member_length FROM cold_members
		WHERE media_asset_id = ? AND state = 'live'`, hash).Scan(&packID, &memberLen); err == nil {
		if _, err := tx.Exec(`UPDATE cold_members SET state = 'retired' WHERE media_asset_id = ?`, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE cold_packs SET dead_bytes = dead_bytes + ? WHERE id = ?`,
			memberLen, packID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if tier == TierWarm {
		_ = os.Remove(archive.String)
	}
	return nil
}

// TierStats reports the storage ladder for dashboards and CLI.
type TierStats struct {
	HotAssets    int64 `json:"hot_assets"`
	HotBytes     int64 `json:"hot_bytes"`
	WarmAssets   int64 `json:"warm_assets"`
	WarmBytes    int64 `json:"warm_bytes"`
	ColdAssets   int64 `json:"cold_assets"`
	ColdBytes    int64 `json:"cold_bytes"`
	ArchiveBytes int64 `json:"archive_bytes"`
	SavedBytes   int64 `json:"saved_bytes"`
	Packs        int64 `json:"packs"`
	PackBytes    int64 `json:"pack_bytes"`
	CacheBytes   int64 `json:"rehydrated_cache_bytes"`
	PrimaryFree  int64 `json:"primary_free_bytes"`
	PrimaryTotal int64 `json:"primary_total_bytes"`
}

func (s *Service) TierStats() (*TierStats, error) {
	st := &TierStats{}
	err := s.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN tier = 'hot' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN tier = 'hot' THEN file_size ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN tier = 'warm' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN tier = 'warm' THEN file_size ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN tier = 'cold' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN tier = 'cold' THEN file_size ELSE 0 END), 0),
		COALESCE(SUM(archive_size), 0)
		FROM media_assets`).Scan(&st.HotAssets, &st.HotBytes,
		&st.WarmAssets, &st.WarmBytes, &st.ColdAssets, &st.ColdBytes, &st.ArchiveBytes)
	if err != nil {
		return nil, err
	}
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(live_bytes + dead_bytes), 0)
		FROM cold_packs WHERE status != 'retired'`).Scan(&st.Packs, &st.PackBytes)
	st.CacheBytes = dirSize(s.rehydratedDir())
	var fs syscall.Statfs_t
	if syscall.Statfs(s.storageRoot, &fs) == nil {
		st.PrimaryFree = int64(fs.Bavail) * int64(fs.Bsize)
		st.PrimaryTotal = int64(fs.Blocks) * int64(fs.Bsize)
	}
	origTotal := st.HotBytes + st.WarmBytes + st.ColdBytes
	storedTotal := st.HotBytes + st.ArchiveBytes
	if origTotal > storedTotal {
		st.SavedBytes = origTotal - storedTotal
	}
	return st, nil
}

// TierPassResult summarizes one bounded maintenance run.
type TierPassResult struct {
	Demoted   []string `json:"demoted"`
	Promoted  []string `json:"promoted"`
	Packed    []string `json:"packed"`
	Errors    []string `json:"errors,omitempty"`
	CacheTrim bool     `json:"cache_trimmed"`
}

// RunTierPass performs one bounded maintenance cycle: promote frequently
// accessed warm/cold assets, demote quiet hot assets when over target or under
// disk pressure, then trim the rehydrated cache. Each step is idempotent and
// limited to `limit` transitions so the interactive path stays responsive.
func (s *Service) RunTierPass(limit int) (*TierPassResult, error) {
	if limit < 1 {
		limit = 25
	}
	cfg := tierConfigFromEnv()
	res := &TierPassResult{}

	// Promote: non-hot assets whose recent access count crossed the threshold.
	rows, err := s.db.Query(`SELECT id FROM media_assets WHERE tier != 'hot'
		AND access_count >= ? LIMIT ?`, cfg.promoteAfter, limit)
	if err != nil {
		return nil, err
	}
	var promoteIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			promoteIDs = append(promoteIDs, id)
		}
	}
	rows.Close()
	for _, id := range promoteIDs {
		if err := s.PromoteAsset(id); err != nil {
			res.Errors = append(res.Errors, "promote "+id[:12]+": "+err.Error())
		} else {
			res.Promoted = append(res.Promoted, id)
		}
	}

	// Demote count = overflow past target + pressure relief.
	var hotCount int64
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM media_assets WHERE tier = 'hot'`).Scan(&hotCount)
	demoteNeed := int(hotCount) - cfg.hotTarget
	if freePct := s.primaryFreePercent(); freePct >= 0 && freePct < cfg.minFreePercent {
		demoteNeed += limit // keep demoting in bounded passes until relieved
	}
	if demoteNeed > limit {
		demoteNeed = limit
	}
	if demoteNeed > 0 {
		graceBefore := time.Now().UTC().Add(-time.Duration(cfg.hotGraceDays) * 24 * time.Hour)
		rows, err := s.db.Query(`SELECT id FROM media_assets WHERE tier = 'hot'
			AND preview_status IN ('ready', 'unavailable')
			AND COALESCE(last_accessed_at, imported_at) < ?
			ORDER BY access_count ASC, COALESCE(last_accessed_at, imported_at) ASC
			LIMIT ?`, graceBefore, demoteNeed)
		if err != nil {
			return res, err
		}
		var demoteIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				demoteIDs = append(demoteIDs, id)
			}
		}
		rows.Close()
		for _, id := range demoteIDs {
			if err := s.DemoteAsset(id); err != nil {
				res.Errors = append(res.Errors, "demote "+id[:12]+": "+err.Error())
			} else {
				res.Demoted = append(res.Demoted, id)
			}
		}
	}

	res.CacheTrim = s.SweepRehydrated(cfg.rehydrateCacheMB)
	return res, nil
}

func (s *Service) primaryFreePercent() int64 {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(s.storageRoot, &fs); err != nil {
		return -1
	}
	if fs.Blocks == 0 {
		return -1
	}
	return int64(fs.Bavail) * 100 / int64(fs.Blocks)
}

// SweepRehydrated evicts oldest decode-cache entries until under budgetMB.
// Returns true when anything was removed.
func (s *Service) SweepRehydrated(budgetMB int64) bool {
	root := s.rehydratedDir()
	type entry struct {
		path  string
		size  int64
		mtime time.Time
	}
	var entries []entry
	var total int64
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			entries = append(entries, entry{p, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	budget := budgetMB << 20
	if total <= budget {
		return false
	}
	// oldest first
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].mtime.Before(entries[i].mtime) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}
	trimmed := false
	for _, e := range entries {
		if total <= budget {
			break
		}
		if os.Remove(e.path) == nil {
			total -= e.size
			trimmed = true
		}
	}
	return trimmed
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}
