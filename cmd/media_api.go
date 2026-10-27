package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"fotoro/internal/auth"
	"fotoro/internal/media"
)

func registerMediaV1Routes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("/api/v1/pairing/sessions", s.withAuth(s.handleCreatePairingSession))
	mux.HandleFunc("/api/v1/pairing/redeem", s.handleRedeemPairingSession)
	mux.HandleFunc("/api/v1/media", s.withAuth(s.handleMediaList))
	mux.HandleFunc("/api/v1/media/", s.withAuth(s.handleMediaItem))
	mux.HandleFunc("/api/v1/uploads", s.withAuth(s.handleUploadCollection))
	mux.HandleFunc("/api/v1/uploads/", s.withAuth(s.handleUploadItem))
	mux.HandleFunc("/api/v1/device-assets/status", s.withAuth(s.handleDeviceAssetStatus))
	mux.HandleFunc("/api/v1/me", s.withAuth(s.handleV1Me))
}

func (s *Server) handleV1Me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	user := auth.UserFromRequest(r)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":        user,
		"device_id":   deviceIDFromUser(user),
		"api_version": "v1",
	})
}

func (s *Server) handleMediaList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	month := strings.TrimSpace(r.URL.Query().Get("month"))
	if month != "" && (len(month) != 7 || month[4] != '-') {
		http.Error(w, `{"error":"month must be YYYY-MM"}`, http.StatusBadRequest)
		return
	}
	deviceID := strings.TrimSpace(r.URL.Query().Get("device_id"))
	assets, total, err := s.media.ListAssets(page, limit, kind, deviceID, month)
	if err != nil {
		http.Error(w, `{"error":"could not list media"}`, http.StatusInternalServerError)
		return
	}
	s.attachDirectMediaLinks(assets)
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"media": assets,
		"page":  page,
		"limit": limit,
		"total": total,
	})
}

func (s *Server) handleMediaItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/media/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"missing media id"}`, http.StatusBadRequest)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, `{"error":"GET only"}`, http.StatusMethodNotAllowed)
			return
		}
		asset, err := s.media.GetAsset(id)
		if err != nil {
			writeMediaError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, asset)
		return
	}
	switch parts[1] {
	case "content":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, `{"error":"GET or HEAD only"}`, http.StatusMethodNotAllowed)
			return
		}
		mediaPath, mimeType, err := s.media.ContentPath(id)
		if err != nil {
			writeMediaError(w, err)
			return
		}
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.ServeFile(w, r, mediaPath) // Supports byte ranges for video playback.
	case "thumbnail":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, `{"error":"GET or HEAD only"}`, http.StatusMethodNotAllowed)
			return
		}
		thumbPath, err := s.media.ThumbnailPath(id, r.URL.Query().Get("size"))
		if err != nil {
			writeMediaError(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeFile(w, r, thumbPath)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleUploadCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	var req media.CreateUploadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	user := auth.UserFromRequest(r)
	ownerID := "local"
	if user != nil && user.ID != "" {
		ownerID = user.ID
	}
	if deviceID := deviceIDFromUser(user); deviceID != "" {
		req.DeviceID = deviceID
	}
	upload, err := s.media.CreateUpload(ownerID, req)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		return
	}
	w.Header().Set("Location", "/api/v1/uploads/"+upload.ID)
	w.Header().Set("Upload-Offset", "0")
	w.Header().Set("Upload-Length", strconv.FormatInt(upload.ExpectedSize, 10))
	writeJSON(w, http.StatusCreated, upload)
}

func (s *Server) handleUploadItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/uploads/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, `{"error":"missing upload id"}`, http.StatusBadRequest)
		return
	}
	id := parts[0]
	user := auth.UserFromRequest(r)
	ownerID := "local"
	if user != nil && user.ID != "" {
		ownerID = user.ID
	}
	if len(parts) == 2 && parts[1] == "complete" {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
			return
		}
		result, err := s.media.CompleteUpload(id, ownerID)
		if err != nil {
			writeUploadError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodHead, http.MethodGet:
		upload, err := s.media.GetUpload(id, ownerID)
		if err != nil {
			writeUploadError(w, err)
			return
		}
		w.Header().Set("Upload-Offset", strconv.FormatInt(upload.ReceivedSize, 10))
		w.Header().Set("Upload-Length", strconv.FormatInt(upload.ExpectedSize, 10))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, upload)
	case http.MethodPatch:
		offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || offset < 0 {
			http.Error(w, `{"error":"valid Upload-Offset header required"}`, http.StatusBadRequest)
			return
		}
		body := http.MaxBytesReader(w, r.Body, media.MaxChunkSize+1)
		data, err := io.ReadAll(body)
		if err != nil || int64(len(data)) > media.MaxChunkSize {
			http.Error(w, `{"error":"chunk exceeds 16 MiB"}`, http.StatusRequestEntityTooLarge)
			return
		}
		upload, err := s.media.AppendChunk(id, ownerID, offset, data)
		if err != nil {
			if errors.Is(err, media.ErrOffset) && upload != nil {
				w.Header().Set("Upload-Offset", strconv.FormatInt(upload.ReceivedSize, 10))
			}
			writeUploadError(w, err)
			return
		}
		w.Header().Set("Upload-Offset", strconv.FormatInt(upload.ReceivedSize, 10))
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.media.CancelUpload(id, ownerID); err != nil {
			writeUploadError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, `{"error":"GET, HEAD, PATCH, or DELETE only"}`, http.StatusMethodNotAllowed)
	}
}

func writeMediaError(w http.ResponseWriter, err error) {
	if errors.Is(err, media.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		http.Error(w, `{"error":"media not found"}`, http.StatusNotFound)
		return
	}
	http.Error(w, `{"error":"media request failed"}`, http.StatusInternalServerError)
}

func writeUploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, media.ErrNotFound):
		http.Error(w, `{"error":"upload not found"}`, http.StatusNotFound)
	case errors.Is(err, media.ErrForbidden):
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	case errors.Is(err, media.ErrOffset):
		http.Error(w, `{"error":"upload offset mismatch"}`, http.StatusConflict)
	case errors.Is(err, media.ErrUploadExpired):
		http.Error(w, `{"error":"upload session expired"}`, http.StatusGone)
	default:
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
	}
}

// Ensure filepath remains linked in this package's generated API docs.
var _ = filepath.Separator
