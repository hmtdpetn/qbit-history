package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
	"strconv"

	"golang.org/x/crypto/bcrypt"
)

func Random(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic("system randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
// PermissionError reports a master key that group or others can read. Some
// filesystems (Windows bind mounts, SMB) cannot represent Unix modes at all and
// always report 0777; see LoadKeyAllowingWorldReadable.
type PermissionError struct {
	Path string
	Mode os.FileMode
}

func (e *PermissionError) Error() string {
	return "master key " + e.Path + " has mode " + e.Mode.String() + "; it must not be readable by group or others (chmod 600)"
}

func LoadKey(path string) ([]byte, error) { return loadKey(path, false) }

// LoadKeyAllowingWorldReadable skips the permission check. Only for local
// testing on filesystems that cannot store Unix modes; never on the server.
func LoadKeyAllowingWorldReadable(path string) ([]byte, error) { return loadKey(path, true) }

func loadKey(path string, skipPerm bool) ([]byte, error) {
	st, e := os.Stat(path)
	if e != nil {
		return nil, errors.New("master key " + path + " is missing; create it with scripts/init-secrets.sh")
	}
	if !skipPerm && runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0 {
		return nil, &PermissionError{Path: path, Mode: st.Mode().Perm()}
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, errors.New("master key " + path + " is unreadable by uid " + strconv.Itoa(os.Getuid()) + ": " + e.Error())
	}
	if len(b) != 32 {
		return nil, errors.New("master key " + path + " is " + strconv.Itoa(len(b)) + " bytes; it must be exactly 32 random bytes")
	}
	return b, nil
}
func CreateKey(path string) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	b := make([]byte, 32)
	if _, e = rand.Read(b); e == nil {
		_, e = f.Write(b)
	}
	return e
}
func Encrypt(key []byte, id, secret string) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return g.Seal(nonce, nonce, []byte(secret), []byte(id)), nil
}
func Decrypt(key []byte, id string, data []byte) (string, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return "", errors.New("credential_key_error")
	}
	g, e := cipher.NewGCM(b)
	if e != nil || len(data) < g.NonceSize() {
		return "", errors.New("credential_unavailable")
	}
	out, e := g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte(id))
	if e != nil {
		return "", errors.New("credential_decryption_failed")
	}
	return string(out), nil
}
func HashPassword(password string) ([]byte, error) {
	if len(password) < 12 || len(password) > 72 {
		return nil, errors.New("password_length_12_to_72")
	}
	return bcrypt.GenerateFromPassword([]byte(password), 12)
}
func CheckPassword(hash []byte, pw string) bool {
	return bcrypt.CompareHashAndPassword(hash, []byte(pw)) == nil
}
func Digest(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
