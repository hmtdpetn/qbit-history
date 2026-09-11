package store

import "qbit-history/internal/model"

func (s *Store) FindInstance(id string) (model.Instance, error) {
	var i model.Instance
	var enabled int
	var secret []byte
	if e := s.Read.QueryRow("SELECT id,name,base_url,username,secret,enabled FROM instance WHERE id=? AND base_url!=''", id).Scan(&i.ID, &i.Name, &i.BaseURL, &i.Username, &secret, &enabled); e != nil {
		return i, e
	}
	i.Secret = secret
	i.PollEnabled = enabled != 0
	i.CredentialsSaved = len(secret) > 0
	return i, nil
}
func (s *Store) SaveInstanceKeepingSecret(i model.Instance, keep bool) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	if keep {
		_, e := s.DB.Exec("UPDATE instance SET name=?,base_url=?,username=?,enabled=? WHERE id=?", i.Name, i.BaseURL, i.Username, boolInt(i.PollEnabled), i.ID)
		return e
	}
	_, e := s.DB.Exec("UPDATE instance SET name=?,base_url=?,username=?,secret=?,enabled=? WHERE id=?", i.Name, i.BaseURL, i.Username, i.Secret, boolInt(i.PollEnabled), i.ID)
	return e
}
