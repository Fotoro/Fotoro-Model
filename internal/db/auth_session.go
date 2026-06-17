package db

import "database/sql"

type AuthSession struct {
	AccessToken  string
	RefreshToken string
	UserID       string
	Email        string
	Name         string
	AvatarURL    string
}

func (d *DB) SaveAuthSession(s AuthSession) error {
	_, err := d.Exec(`
		UPDATE server_config SET
			auth_access_token = ?,
			auth_refresh_token = ?,
			auth_user_id = ?,
			auth_user_email = ?,
			auth_user_name = ?,
			auth_user_avatar = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = 1
	`, s.AccessToken, s.RefreshToken, s.UserID, s.Email, s.Name, s.AvatarURL)
	return err
}

func (d *DB) GetAuthSession() (*AuthSession, error) {
	var s AuthSession
	var access, refresh, uid, email, name, avatar sql.NullString
	err := d.QueryRow(`
		SELECT auth_access_token, auth_refresh_token, auth_user_id,
		       auth_user_email, auth_user_name, auth_user_avatar
		FROM server_config WHERE id = 1
	`).Scan(&access, &refresh, &uid, &email, &name, &avatar)
	if err != nil {
		return nil, err
	}
	if !access.Valid || access.String == "" {
		return nil, nil
	}
	s.AccessToken = access.String
	if refresh.Valid {
		s.RefreshToken = refresh.String
	}
	if uid.Valid {
		s.UserID = uid.String
	}
	if email.Valid {
		s.Email = email.String
	}
	if name.Valid {
		s.Name = name.String
	}
	if avatar.Valid {
		s.AvatarURL = avatar.String
	}
	return &s, nil
}

func (d *DB) IsSetupComplete() (bool, error) {
	var n int
	err := d.QueryRow(`SELECT setup_complete FROM server_config WHERE id = 1`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
