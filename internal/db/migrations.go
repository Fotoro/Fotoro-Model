package db

import (
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
		OnlineDBEndpoint string
		OnlineDBToken    string
	}

	err := d.QueryRow(`
		SELECT setup_complete, setup_step, server_name, tailscale_enabled, tailscale_ip,
		       tailnet_name, funnel_enabled, serve_enabled, public_url, tailnet_url,
		       online_db_endpoint, online_db_token
		FROM server_config WHERE id = 1
	`).Scan(&cfg.SetupComplete, &cfg.SetupStep, &cfg.ServerName, &cfg.TailscaleEnabled,
		&cfg.TailscaleIP, &cfg.TailnetName, &cfg.FunnelEnabled, &cfg.ServeEnabled,
		&cfg.PublicURL, &cfg.TailnetURL, &cfg.OnlineDBEndpoint, &cfg.OnlineDBToken)

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
		Name      string
		CreatedAt time.Time
		LastLogin time.Time
		IsActive  int
	}

	err := d.QueryRow(`
		SELECT id, email, name, created_at, last_login, is_active
		FROM users WHERE email = ?
	`, email).Scan(&user.ID, &user.Email, &user.Name, &user.CreatedAt, &user.LastLogin, &user.IsActive)

	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"id":         user.ID,
		"email":      user.Email,
		"name":       user.Name,
		"created_at": user.CreatedAt,
		"last_login": user.LastLogin,
		"is_active":  user.IsActive == 1,
	}, nil
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
