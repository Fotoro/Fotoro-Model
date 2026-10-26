package cmd

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"fotoro/internal/auth"
)

// The phone calls this in batches after MediaStore discovery. Missing assets
// are uploaded; primary-only assets remain queued for server-side backup.
func (s *Server) handleDeviceAssetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	deviceID := deviceIDFromUser(auth.UserFromRequest(r))
	if deviceID == "" {
		http.Error(w, `{"error":"device credential required"}`, http.StatusForbidden)
		return
	}
	var body struct {
		Assets []struct {
			DeviceAssetID string `json:"device_asset_id"`
			Size          int64  `json:"size"`
			SHA256        string `json:"sha256"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil || len(body.Assets) > 200 {
		http.Error(w, `{"error":"provide at most 200 assets"}`, http.StatusBadRequest)
		return
	}
	type item struct {
		DeviceAssetID string `json:"device_asset_id"`
		MediaAssetID  string `json:"media_asset_id,omitempty"`
		State         string `json:"state"`
		SafeToDelete  bool   `json:"safe_to_delete"`
	}
	results := make([]item, 0, len(body.Assets))
	for _, requestAsset := range body.Assets {
		id := strings.TrimSpace(requestAsset.DeviceAssetID)
		if id == "" || len(id) > 256 || requestAsset.Size < 0 {
			http.Error(w, `{"error":"invalid device asset id"}`, http.StatusBadRequest)
			return
		}
		result := item{DeviceAssetID: id, State: "needs_upload"}
		var assetID, storedHash string
		var storedSize int64
		err := s.db.QueryRow(`SELECT COALESCE(media_asset_id, ''), file_size, COALESCE(sha256, '') FROM device_assets
			WHERE device_id = ? AND device_asset_id = ?`, deviceID, id).Scan(&assetID, &storedSize, &storedHash)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, `{"error":"device status unavailable"}`, http.StatusInternalServerError)
			return
		}
		if assetID != "" && requestAsset.Size == storedSize &&
			(requestAsset.SHA256 == "" || strings.EqualFold(requestAsset.SHA256, storedHash)) {
			asset, err := s.media.GetAsset(assetID)
			if err == nil {
				result.MediaAssetID = assetID
				result.SafeToDelete = asset.SafeToDelete
				if asset.SafeToDelete {
					result.State = "backed_up"
				} else if asset.StorageCopies > 0 {
					result.State = "primary_verified"
				}
			}
		}
		results = append(results, result)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"device_id": deviceID, "assets": results})
}
