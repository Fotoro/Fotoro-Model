package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (d *DB) ExtendedMigrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			name TEXT,
			password_hash TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_login DATETIME,
			is_active INTEGER DEFAULT 1,
			tailscale_auth_key TEXT,
			tailscale_ip TEXT,
			tailnet_name TEXT,
			node_name TEXT DEFAULT 'fotoro-server'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`,
		`CREATE TABLE IF NOT EXISTS mobile_devices (
			id INTEGER PRIMARY KEY,
			user_id INTEGER REFERENCES users(id),
			device_name TEXT,
			device_id TEXT UNIQUE NOT NULL,
			platform TEXT,
			paired_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_seen DATETIME,
			is_active INTEGER DEFAULT 1,
			push_token TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_mobile_devices_user ON mobile_devices(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_mobile_devices_device_id ON mobile_devices(device_id)`,
		`CREATE TABLE IF NOT EXISTS mobile_pairing (
			id INTEGER PRIMARY KEY,
			code TEXT UNIQUE NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL,
			used INTEGER DEFAULT 0,
			device_id TEXT,
			user_id INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pairing_code ON mobile_pairing(code)`,
		`CREATE INDEX IF NOT EXISTS idx_pairing_expires ON mobile_pairing(expires_at)`,
		`CREATE TABLE IF NOT EXISTS server_config (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			setup_complete INTEGER DEFAULT 0,
			setup_step TEXT DEFAULT 'welcome',
			server_name TEXT DEFAULT 'fotoro-server',
			tailscale_enabled INTEGER DEFAULT 0,
			tailscale_ip TEXT,
			tailnet_name TEXT,
			funnel_enabled INTEGER DEFAULT 0,
			serve_enabled INTEGER DEFAULT 0,
			public_url TEXT,
			tailnet_url TEXT,
			online_db_endpoint TEXT,
			online_db_token TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`INSERT OR IGNORE INTO server_config (id) VALUES (1)`,
		`ALTER TABLE server_config ADD COLUMN auth_access_token TEXT`,
		`ALTER TABLE server_config ADD COLUMN auth_refresh_token TEXT`,
		`ALTER TABLE server_config ADD COLUMN auth_user_id TEXT`,
		`ALTER TABLE server_config ADD COLUMN auth_user_email TEXT`,
		`ALTER TABLE server_config ADD COLUMN auth_user_name TEXT`,
		`ALTER TABLE server_config ADD COLUMN auth_user_avatar TEXT`,
		`ALTER TABLE server_config ADD COLUMN schedule_time TEXT DEFAULT '02:00'`,
		`ALTER TABLE server_config ADD COLUMN schedule_days TEXT DEFAULT '1,2,3,4,5'`,
		`ALTER TABLE server_config ADD COLUMN tailnet_serve_url TEXT`,
		`CREATE TABLE IF NOT EXISTS offline_cache (
			id INTEGER PRIMARY KEY,
			hash TEXT NOT NULL,
			device_id TEXT NOT NULL,
			cached_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_accessed DATETIME,
			access_count INTEGER DEFAULT 1,
			UNIQUE(hash, device_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_offline_cache_device ON offline_cache(device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_offline_cache_hash ON offline_cache(hash)`,
		`CREATE TABLE IF NOT EXISTS activity_log (
			id INTEGER PRIMARY KEY,
			action TEXT NOT NULL,
			entity_type TEXT,
			entity_id TEXT,
			details TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_activity_time ON activity_log(created_at)`,
		`CREATE TABLE IF NOT EXISTS favorites (
			user_id INTEGER NOT NULL,
			hash TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, hash)
		)`,
		`CREATE TABLE IF NOT EXISTS albums (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL,
			name TEXT NOT NULL,
			description TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS album_images (
			album_id INTEGER NOT NULL,
			hash TEXT NOT NULL,
			added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (album_id, hash)
		)`,
		`CREATE TABLE IF NOT EXISTS media_assets (
			id TEXT PRIMARY KEY,
			hash TEXT UNIQUE NOT NULL,
			original_name TEXT NOT NULL DEFAULT '',
			storage_path TEXT NOT NULL,
			media_kind TEXT NOT NULL DEFAULT 'other',
			mime_type TEXT NOT NULL DEFAULT 'application/octet-stream',
			file_size INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'verified',
			captured_at DATETIME,
			imported_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			width INTEGER,
			height INTEGER,
			duration_ms INTEGER,
			checksum_verified INTEGER NOT NULL DEFAULT 0,
			preview_status TEXT NOT NULL DEFAULT 'pending',
			source_device_id TEXT,
			device_asset_id TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(source_device_id, device_asset_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_media_assets_imported ON media_assets(imported_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_media_assets_kind ON media_assets(media_kind)`,
		`CREATE INDEX IF NOT EXISTS idx_media_assets_captured ON media_assets(captured_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_media_assets_timeline ON media_assets(COALESCE(captured_at, imported_at) DESC, id DESC)`,
		`CREATE TABLE IF NOT EXISTS device_assets (
			device_id TEXT NOT NULL,
			device_asset_id TEXT NOT NULL,
			media_asset_id TEXT,
			original_name TEXT,
			local_uri TEXT,
			file_size INTEGER NOT NULL DEFAULT 0,
			sha256 TEXT,
			sync_status TEXT NOT NULL DEFAULT 'discovered',
			verified_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (device_id, device_asset_id),
			FOREIGN KEY (media_asset_id) REFERENCES media_assets(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_device_assets_status ON device_assets(device_id, sync_status)`,
		`CREATE TABLE IF NOT EXISTS upload_sessions (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			device_id TEXT,
			device_asset_id TEXT,
			original_name TEXT NOT NULL,
			mime_type TEXT NOT NULL DEFAULT 'application/octet-stream',
			expected_size INTEGER NOT NULL,
			expected_sha256 TEXT,
			received_size INTEGER NOT NULL DEFAULT 0,
			temp_path TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'uploading',
			media_asset_id TEXT,
			error_message TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL,
			FOREIGN KEY (media_asset_id) REFERENCES media_assets(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_upload_sessions_owner ON upload_sessions(owner_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_upload_sessions_expiry ON upload_sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS derivatives (
			id INTEGER PRIMARY KEY,
			media_asset_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			storage_path TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			width INTEGER,
			height INTEGER,
			file_size INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(media_asset_id, kind),
			FOREIGN KEY (media_asset_id) REFERENCES media_assets(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS analysis_jobs (
			id INTEGER PRIMARY KEY,
			media_asset_id TEXT NOT NULL,
			job_type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'deferred',
			error_message TEXT,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(media_asset_id, job_type),
			FOREIGN KEY (media_asset_id) REFERENCES media_assets(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS storage_copies (
			id INTEGER PRIMARY KEY,
			media_asset_id TEXT NOT NULL,
			storage_name TEXT NOT NULL,
			storage_path TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'verified',
			checksum_verified INTEGER NOT NULL DEFAULT 0,
			verified_at DATETIME,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(media_asset_id, storage_name),
			FOREIGN KEY (media_asset_id) REFERENCES media_assets(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS device_credentials (
			device_id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL DEFAULT '',
			token_hash TEXT UNIQUE NOT NULL,
			name TEXT NOT NULL,
			platform TEXT NOT NULL,
			scopes TEXT NOT NULL DEFAULT 'media:read,media:write',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_used_at DATETIME,
			revoked_at DATETIME
		)`,
		`ALTER TABLE device_credentials ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS pairing_sessions (
			id TEXT PRIMARY KEY,
			secret_hash TEXT UNIQUE NOT NULL,
			created_by TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL,
			used_at DATETIME,
			device_id TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pairing_sessions_expiry ON pairing_sessions(expires_at)`,
	}

	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") ||
				strings.Contains(err.Error(), "already exists") ||
				strings.Contains(err.Error(), "SQL logic error: no such column") ||
				strings.Contains(err.Error(), "UNIQUE constraint failed") {
				continue
			}
			return fmt.Errorf("extended migrate: %q: %w", stmt[:50], err)
		}
	}
	// Existing paired devices remain linked to the account used during setup.
	if _, err := d.Exec(`UPDATE device_credentials SET owner_id =
		(SELECT COALESCE(auth_user_id, '') FROM server_config WHERE id = 1)
		WHERE owner_id = ''`); err != nil {
		return fmt.Errorf("link existing devices: %w", err)
	}

	// Existing image libraries remain available through the canonical media API.
	// Paths are preserved; newly uploaded assets are copied into library/originals.
	_, err := d.Exec(`
		INSERT OR IGNORE INTO media_assets (
			id, hash, original_name, storage_path, media_kind, mime_type, file_size,
			status, captured_at, imported_at, checksum_verified, preview_status,
			created_at, updated_at
		)
		SELECT hash, hash, path, path, 'image',
			CASE WHEN content_type IS NULL OR content_type = '' OR content_type = 'unknown'
				THEN 'image/jpeg' ELSE content_type END,
			0, CASE WHEN category = 'failed' THEN 'stored' ELSE 'verified' END,
			taken_at, COALESCE(processed_at, created_at), 1, 'ready',
			created_at, COALESCE(processed_at, created_at)
		FROM images
	`)
	if err != nil {
		return fmt.Errorf("backfill media assets: %w", err)
	}
	return nil
}

func (d *DB) GetServerConfig() (map[string]interface{}, error) {
	var cfg struct {
		SetupComplete    int
		SetupStep        string
		ServerName       string
		TailscaleEnabled int
		TailscaleIP      string
		TailnetName      string
		FunnelEnabled    int
		ServeEnabled     int
		PublicURL        string
		TailnetURL       string
		TailnetServeURL  string
		OnlineDBEndpoint string
		OnlineDBToken    string
	}

	err := d.QueryRow(`
		SELECT setup_complete, setup_step, server_name, tailscale_enabled, tailscale_ip,
		       tailnet_name, funnel_enabled, serve_enabled, public_url, tailnet_url,
		       COALESCE(tailnet_serve_url, ''), online_db_endpoint, online_db_token
		FROM server_config WHERE id = 1
	`).Scan(&cfg.SetupComplete, &cfg.SetupStep, &cfg.ServerName, &cfg.TailscaleEnabled,
		&cfg.TailscaleIP, &cfg.TailnetName, &cfg.FunnelEnabled, &cfg.ServeEnabled,
		&cfg.PublicURL, &cfg.TailnetURL, &cfg.TailnetServeURL, &cfg.OnlineDBEndpoint, &cfg.OnlineDBToken)

	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"setup_complete":     cfg.SetupComplete == 1,
		"setup_step":         cfg.SetupStep,
		"server_name":        cfg.ServerName,
		"tailscale_enabled":  cfg.TailscaleEnabled == 1,
		"tailscale_ip":       cfg.TailscaleIP,
		"tailnet_name":       cfg.TailnetName,
		"funnel_enabled":     cfg.FunnelEnabled == 1,
		"serve_enabled":      cfg.ServeEnabled == 1,
		"public_url":         cfg.PublicURL,
		"tailnet_url":        cfg.TailnetURL,
		"tailnet_serve_url":  cfg.TailnetServeURL,
		"online_db_endpoint": cfg.OnlineDBEndpoint,
	}, nil
}

func (d *DB) UpdateServerConfig(updates map[string]interface{}) error {
	fields := []string{}
	values := []interface{}{}

	for k, v := range updates {
		fields = append(fields, k+" = ?")
		values = append(values, v)
	}

	if len(fields) == 0 {
		return nil
	}

	query := "UPDATE server_config SET " + strings.Join(fields, ", ") + ", updated_at = CURRENT_TIMESTAMP WHERE id = 1"
	_, err := d.Exec(query, values...)
	return err
}

func (d *DB) CreateUser(email, name, passwordHash string) (int64, error) {
	res, err := d.Exec(`
		INSERT INTO users (email, name, password_hash, created_at)
		VALUES (?, ?, ?, ?)
	`, email, name, passwordHash, time.Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) GetUserByEmail(email string) (map[string]interface{}, error) {
	var user struct {
		ID        int
		Email     string
		Name      sql.NullString
		CreatedAt time.Time
		LastLogin sql.NullTime
		IsActive  int
	}

	err := d.QueryRow(`
		SELECT id, email, name, created_at, last_login, is_active
		FROM users WHERE email = ?
	`, email).Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt, &user.LastLogin, &user.IsActive)

	if err != nil {
		return nil, err
	}

	out := map[string]interface{}{
		"id":         user.ID,
		"email":      user.Email,
		"created_at": user.CreatedAt,
		"is_active":  user.IsActive == 1,
	}
	if user.Name.Valid {
		out["name"] = user.Name.String
	}
	if user.LastLogin.Valid {
		out["last_login"] = user.LastLogin.Time
	}
	return out, nil
}

// GetOrCreateUser returns an existing user by email or inserts a new row.
func (d *DB) GetOrCreateUser(email, name, passwordHash string) (int64, error) {
	if existing, err := d.GetUserByEmail(email); err == nil {
		if id := mapUserID(existing); id > 0 {
			return id, nil
		}
	}
	id, err := d.CreateUser(email, name, passwordHash)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		if existing, err2 := d.GetUserByEmail(email); err2 == nil {
			if id := mapUserID(existing); id > 0 {
				return id, nil
			}
		}
	}
	return id, err
}

func mapUserID(m map[string]interface{}) int64 {
	switch v := m["id"].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func (d *DB) UpdateUserTailscale(userID int, ip, tailnet, nodeName string) error {
	_, err := d.Exec(`
		UPDATE users SET tailscale_ip = ?, tailnet_name = ?, node_name = ? WHERE id = ?
	`, ip, tailnet, nodeName, userID)
	return err
}

func (d *DB) RegisterDevice(userID int, deviceName, deviceID, platform string) error {
	_, err := d.Exec(`
		INSERT INTO mobile_devices (user_id, device_name, device_id, platform, last_seen)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			user_id = excluded.user_id,
			device_name = excluded.device_name,
			last_seen = excluded.last_seen,
			is_active = 1
	`, userID, deviceName, deviceID, platform, time.Now())
	return err
}

func (d *DB) LogActivity(action, entityType, entityID, details string) {
	d.Exec(`
		INSERT INTO activity_log (action, entity_type, entity_id, details, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, action, entityType, entityID, details, time.Now())
}
