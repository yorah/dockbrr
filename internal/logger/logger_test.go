package logger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitWritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs", "dockbrr.log")
	got, err := Init(Config{Path: path, Level: "info", MaxSizeMB: 1, MaxBackups: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Dir(path) {
		t.Fatalf("dir = %q, want %q", got, filepath.Dir(path))
	}
	Infof("hello %s", "world")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "hello world") {
		t.Errorf("log file missing message; got %q", b)
	}
}

func TestSetLevelFiltersBelow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dockbrr.log")
	if _, err := Init(Config{Path: path, Level: "info", MaxSizeMB: 1, MaxBackups: 1}); err != nil {
		t.Fatal(err)
	}
	Debugf("should-not-appear") // below info
	if err := SetLevel("debug"); err != nil {
		t.Fatal(err)
	}
	Debugf("should-appear-now")
	b, _ := os.ReadFile(path)
	s := string(b)
	if strings.Contains(s, "should-not-appear") {
		t.Error("debug line leaked while level was info")
	}
	if !strings.Contains(s, "should-appear-now") {
		t.Error("debug line missing after SetLevel(debug)")
	}
}

func TestSetLevelRejectsBad(t *testing.T) {
	if err := SetLevel("bogus"); err == nil {
		t.Error("expected error for bad level")
	}
}

func TestFilesNewestFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dockbrr.log")
	if _, err := Init(Config{Path: path, Level: "info", MaxSizeMB: 1, MaxBackups: 1}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "old.log"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "new.log"), []byte("y"), 0o600)
	_ = os.Chtimes(filepath.Join(dir, "old.log"), timeAt(1), timeAt(1))
	_ = os.Chtimes(filepath.Join(dir, "new.log"), timeAt(2), timeAt(2))
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("want >=2 files, got %d", len(files))
	}
	var iNew, iOld = -1, -1
	for i, f := range files {
		if f.Name == "new.log" {
			iNew = i
		}
		if f.Name == "old.log" {
			iOld = i
		}
	}
	if iNew == -1 || iOld == -1 || iNew > iOld {
		t.Errorf("new.log (%d) should sort before old.log (%d)", iNew, iOld)
	}
}

func TestOpenRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dockbrr.log")
	if _, err := Init(Config{Path: path, Level: "info", MaxSizeMB: 1, MaxBackups: 1}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, []byte("data"), 0o600)
	for _, bad := range []string{"../secret", "a/b", "/etc/passwd", "", ".."} {
		if _, err := Open(bad); err == nil {
			t.Errorf("Open(%q) should fail", bad)
		}
	}
	rc, err := Open("dockbrr.log")
	if err != nil {
		t.Fatalf("Open(dockbrr.log) failed: %v", err)
	}
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(b) != "data" {
		t.Errorf("read = %q, want data", b)
	}
}

func timeAt(sec int64) time.Time { return time.Unix(1_700_000_000+sec, 0) }
