package store

import (
	"errors"
	"time"

	"qbit-history/internal/security"
)

func (s *Store) EnsureAdmin(name, password string) error {
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	var n int
	if err := s.Read.QueryRow("SELECT COUNT(*) FROM local_user").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return errors.New("administrator already exists")
	}
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	_, err = s.DB.Exec("INSERT OR IGNORE INTO local_user(id,name,password_hash) VALUES(1,?,?)", name, hash)
	return err
}
func (s *Store) Authenticate(name, password string) bool {
	var user string
	var hash []byte
	if s.Read.QueryRow("SELECT name,password_hash FROM local_user WHERE id=1").Scan(&user, &hash) != nil {
		return false
	}
	return user == name && security.CheckPassword(hash, password)
}
func (s *Store) ChangePassword(password string) error {
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	if _, err = s.DB.Exec("UPDATE local_user SET password_hash=? WHERE id=1", hash); err != nil {
		return err
	}
	_, err = s.DB.Exec("DELETE FROM session")
	return err
}
func (s *Store) CreateSession(tokenHash, csrf string, expires time.Time) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	_, err := s.DB.Exec("INSERT INTO session(token_hash,csrf,expires_ms) VALUES(?,?,?)", tokenHash, csrf, expires.UnixMilli())
	return err
}
func (s *Store) Session(tokenHash string) (string, bool) {
	var csrf string
	var expires int64
	if s.Read.QueryRow("SELECT csrf,expires_ms FROM session WHERE token_hash=?", tokenHash).Scan(&csrf, &expires) != nil {
		return "", false
	}
	if expires <= time.Now().UnixMilli() {
		return "", false
	}
	return csrf, true
}
func (s *Store) DeleteSession(tokenHash string) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	_, e := s.DB.Exec("DELETE FROM session WHERE token_hash=?", tokenHash)
	return e
}
func (s *Store) UserName() string {
	var n string
	s.Read.QueryRow("SELECT name FROM local_user WHERE id=1").Scan(&n)
	return n
}
func (s *Store) UpdatePoll(id string, enabled bool) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	_, e := s.DB.Exec("UPDATE instance SET enabled=? WHERE id=?", boolInt(enabled), id)
	return e
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
