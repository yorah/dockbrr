package secret_test

import (
	"os"
	"path/filepath"
	"testing"

	"dockbrr/internal/secret"
)

func TestLoadOrCreateKeyIsStableAnd0600(t *testing.T) {
	dir := t.TempDir()
	k1, err := secret.LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != 32 {
		t.Fatalf("key length = %d, want 32", len(k1))
	}
	info, err := os.Stat(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %o, want 600", info.Mode().Perm())
	}
	k2, _ := secret.LoadOrCreateKey(dir)
	if string(k1) != string(k2) {
		t.Fatal("key changed on second load")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key, _ := secret.LoadOrCreateKey(t.TempDir())
	s, err := secret.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := s.Seal([]byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Open(enc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("round trip = %q", got)
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	a, _ := secret.NewSealer(mustKey(t))
	b, _ := secret.NewSealer(mustKey(t))
	enc, _ := a.Seal([]byte("secret"))
	if _, err := b.Open(enc); err == nil {
		t.Fatal("expected open with wrong key to fail")
	}
}

func TestNewSealerRejectsNon32ByteKey(t *testing.T) {
	if _, err := secret.NewSealer(make([]byte, 16)); err == nil {
		t.Fatal("expected NewSealer to reject a 16-byte key")
	}
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := secret.LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return k
}
