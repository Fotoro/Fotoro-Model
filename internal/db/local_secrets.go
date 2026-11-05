package db

import (
	"crypto/rand"
	"database/sql"
	"fmt"
)

const endpointEncryptionKeyName = "node_endpoint_aes256_v1"

// GetOrCreateEndpointKey returns a host-local 256-bit key used to encrypt
// Tailscale routing details before the host sends a node sync to the website.
// The key never leaves this local database through the sync API.
func (d *DB) GetOrCreateEndpointKey() ([]byte, error) {
	var key []byte
	err := d.QueryRow(`SELECT value FROM local_secrets WHERE name = ?`, endpointEncryptionKeyName).Scan(&key)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("stored endpoint encryption key has invalid length")
		}
		return key, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("read endpoint encryption key: %w", err)
	}

	generated := make([]byte, 32)
	if _, err := rand.Read(generated); err != nil {
		return nil, fmt.Errorf("generate endpoint encryption key: %w", err)
	}
	if _, err := d.Exec(`INSERT OR IGNORE INTO local_secrets (name, value) VALUES (?, ?)`, endpointEncryptionKeyName, generated); err != nil {
		return nil, fmt.Errorf("save endpoint encryption key: %w", err)
	}
	if err := d.restrictFilePermissions(); err != nil {
		return nil, err
	}

	// A concurrent sync may have created the key first. Always return the
	// stored winner, never the losing in-memory candidate.
	if err := d.QueryRow(`SELECT value FROM local_secrets WHERE name = ?`, endpointEncryptionKeyName).Scan(&key); err != nil {
		return nil, fmt.Errorf("read saved endpoint encryption key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("saved endpoint encryption key has invalid length")
	}
	return key, nil
}
