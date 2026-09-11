package security

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func key(t *testing.T) []byte {
	t.Helper()
	p := filepath.Join(t.TempDir(), "master.key")
	if e := CreateKey(p); e != nil {
		t.Fatal(e)
	}
	k, e := LoadKey(p)
	if e != nil {
		t.Fatal(e)
	}
	return k
}

func TestEncryptRoundTripIsBoundToInstance(t *testing.T) {
	k := key(t)
	secret := "adminadmin"
	a, e := Encrypt(k, "instance-a", secret)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(a, []byte(secret)) {
		t.Fatal("ciphertext contains the plaintext")
	}
	got, e := Decrypt(k, "instance-a", a)
	if e != nil || got != secret {
		t.Fatalf("round trip: %q %v", got, e)
	}
	// The instance id is authenticated additional data: a ciphertext copied to
	// another connection must not decrypt.
	if _, e = Decrypt(k, "instance-b", a); e == nil {
		t.Fatal("ciphertext decrypted under a different instance id")
	}
	// A different key must not decrypt it either.
	if _, e = Decrypt(key(t), "instance-a", a); e == nil {
		t.Fatal("ciphertext decrypted with a foreign master key")
	}
	// Any tampering is detected (AEAD).
	for _, pos := range []int{0, len(a) / 2, len(a) - 1} {
		bad := append([]byte{}, a...)
		bad[pos] ^= 0xff
		if _, e = Decrypt(k, "instance-a", bad); e == nil {
			t.Fatalf("tampered byte %d was accepted", pos)
		}
	}
	if _, e = Decrypt(k, "instance-a", []byte{1, 2, 3}); e == nil {
		t.Fatal("a too-short blob was accepted")
	}
	// Nonces are random: the same secret encrypts differently every time.
	b, _ := Encrypt(k, "instance-a", secret)
	if bytes.Equal(a, b) {
		t.Fatal("nonce reuse: two encryptions produced identical ciphertext")
	}
}

func TestKeyFileHandling(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "master.key")
	if _, e := LoadKey(p); e == nil {
		t.Fatal("missing key file accepted")
	}
	if e := CreateKey(p); e != nil {
		t.Fatal(e)
	}
	if e := CreateKey(p); e == nil {
		t.Fatal("CreateKey overwrote an existing key")
	}
	st, _ := os.Stat(p)
	if st.Size() != 32 {
		t.Fatalf("key is %d bytes, want 32", st.Size())
	}
	if runtime.GOOS != "windows" {
		if st.Mode().Perm() != 0600 {
			t.Fatalf("key mode is %v, want 0600", st.Mode().Perm())
		}
		os.Chmod(p, 0644)
		if _, e := LoadKey(p); e == nil {
			t.Fatal("a world-readable master key was accepted")
		}
		os.Chmod(p, 0600)
	}
	short := filepath.Join(dir, "short.key")
	os.WriteFile(short, []byte("too short"), 0600)
	if _, e := LoadKey(short); e == nil {
		t.Fatal("a key of the wrong length was accepted")
	}
	a, _ := LoadKey(p)
	b, _ := LoadKey(p)
	if !bytes.Equal(a, b) {
		t.Fatal("LoadKey is not stable")
	}
}

func TestPasswordHashing(t *testing.T) {
	if _, e := HashPassword("short"); e == nil {
		t.Fatal("a password under 12 characters was accepted")
	}
	if _, e := HashPassword(string(make([]byte, 100))); e == nil {
		t.Fatal("a password over 72 bytes was accepted")
	}
	h, e := HashPassword("correct-horse-battery")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(h, []byte("correct-horse-battery")) {
		t.Fatal("hash contains the password")
	}
	if !CheckPassword(h, "correct-horse-battery") {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(h, "correct-horse-batterY") || CheckPassword(h, "") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct-horse-battery")
	if bytes.Equal(h, h2) {
		t.Fatal("bcrypt salt is not random")
	}
}

func TestRandomAndDigest(t *testing.T) {
	a, b := Random(32), Random(32)
	if a == b || len(a) < 40 {
		t.Fatalf("Random is not unique or too short: %q %q", a, b)
	}
	d := Digest("token")
	if len(d) != 64 || d == "token" || Digest("token") != d || Digest("token2") == d {
		t.Fatalf("Digest is not a stable 32-byte hex hash: %q", d)
	}
}
