package store_test

import (
	"errors"
	"path/filepath"
	"testing"

	"dockbrr/internal/secret"
	"dockbrr/internal/store"
)

func newSettings(t *testing.T) *store.Settings {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := secret.LoadOrCreateKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secret.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return store.NewSettings(db, sealer)
}

func TestSettingsSetGet(t *testing.T) {
	s := newSettings(t)
	if err := s.Set("poll_interval", "300"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("poll_interval")
	if err != nil {
		t.Fatal(err)
	}
	if got != "300" {
		t.Fatalf("got %q, want 300", got)
	}
}

func TestSettingsUpsertOverwrites(t *testing.T) {
	s := newSettings(t)
	if err := s.Set("k", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("k", "b"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if got != "b" {
		t.Fatalf("got %q, want b", got)
	}
}

func TestSettingsMissingReturnsSentinel(t *testing.T) {
	s := newSettings(t)
	_, err := s.Get("nope")
	if !errors.Is(err, store.ErrSettingNotFound) {
		t.Fatalf("err = %v, want ErrSettingNotFound", err)
	}
}

func TestSettingsSecretRoundTripAndCiphertextAtRest(t *testing.T) {
	s := newSettings(t)
	if err := s.SetSecret("github_token", "ghp_xyz"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSecret("github_token")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ghp_xyz" {
		t.Fatalf("secret round trip = %q", got)
	}
	raw, _ := s.Get("github_token") // stored form must NOT be plaintext
	if raw == "ghp_xyz" {
		t.Fatal("secret stored in plaintext")
	}
}

func TestGetBoolDefault(t *testing.T) {
	s := newSettings(t)
	if !s.GetBoolDefault("write_back_compose", true) {
		t.Error("absent key should return default true")
	}
	if err := s.Set("write_back_compose", "false"); err != nil {
		t.Fatal(err)
	}
	if s.GetBoolDefault("write_back_compose", true) {
		t.Error("set false should override default")
	}
	if err := s.Set("write_back_compose", "true"); err != nil {
		t.Fatal(err)
	}
	if !s.GetBoolDefault("write_back_compose", false) {
		t.Error("set true should override default")
	}
}
