package compose

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const composeFixture = `services:
  web:
    image: nginx:1.31.2   # pinned
    ports:
      - "8080:80"
  cache:
    image: ${REDIS_IMAGE}
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocateImageLineLiteral(t *testing.T) {
	p := writeTemp(t, "compose.yml", composeFixture)
	loc, err := LocateImageLine([]string{p}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if !loc.Rewritable || loc.OldRef != "nginx:1.31.2" || loc.File != p {
		t.Fatalf("got %+v", loc)
	}
	if loc.Line != 3 {
		t.Errorf("Line = %d, want 3", loc.Line)
	}
}

func TestLocateImageLineInterpolatedNotRewritable(t *testing.T) {
	p := writeTemp(t, "compose.yml", composeFixture)
	loc, err := LocateImageLine([]string{p}, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if loc.Rewritable {
		t.Fatalf("interpolated image must not be rewritable: %+v", loc)
	}
}

func TestLocateImageLineMissingService(t *testing.T) {
	p := writeTemp(t, "compose.yml", composeFixture)
	loc, err := LocateImageLine([]string{p}, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if loc.Rewritable {
		t.Fatalf("missing service must not be rewritable: %+v", loc)
	}
}

func TestLocateImageLineLastFileWins(t *testing.T) {
	base := writeTemp(t, "compose.yml", composeFixture)
	over := writeTemp(t, "override.yml", "services:\n  web:\n    image: nginx:1.31.2\n")
	loc, err := LocateImageLine([]string{base, over}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if loc.File != over {
		t.Errorf("last file should win: got %s", loc.File)
	}
}

func TestReplaceImageLinePreservesFormatting(t *testing.T) {
	out, err := ReplaceImageLine(composeFixture, "nginx:1.31.2", "nginx:1.32.0", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image: nginx:1.32.0   # pinned") {
		t.Errorf("comment/formatting not preserved:\n%s", out)
	}
	if strings.Contains(out, "nginx:1.31.2") {
		t.Errorf("old ref still present:\n%s", out)
	}
	if !strings.Contains(out, `- "8080:80"`) {
		t.Errorf("unrelated lines mangled:\n%s", out)
	}
}

func TestReplaceImageLineWrongLineErrors(t *testing.T) {
	if _, err := ReplaceImageLine(composeFixture, "nginx:1.31.2", "nginx:1.32.0", 1); err == nil {
		t.Fatal("expected error when line lacks oldRef")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, "new content"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "new content" {
		t.Errorf("got %q", string(b))
	}
}

func TestWriteFileAtomicPreservesMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "compose.yml")
	// Create file with 0644 mode
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, "new content"); err != nil {
		t.Fatal(err)
	}
	// Verify mode is still 0644
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("permissions changed: got %o, want 0644", perm)
	}
}

func TestWriteFileAtomicNewFileDefaultMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "newfile.yml")
	// Write to non-existent file
	if err := WriteFileAtomic(p, "new content"); err != nil {
		t.Fatal(err)
	}
	// Verify mode is 0644
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("new file mode: got %o, want 0644", perm)
	}
}

// Ownership preservation: with the writer and the file owner being the same
// uid (the only case exercisable without root), the chown path must be a
// no-op that leaves the file owned by us. The cross-uid case (containerized
// dockbrr as root over a user-owned bind mount) is what the code exists for
// and is covered by inspection + the release smoke.
func TestWriteFileAtomicKeepsOwnership(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(p, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, "new"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	if int(sys.Uid) != os.Getuid() || int(sys.Gid) != os.Getgid() {
		t.Fatalf("owner = %d:%d, want %d:%d", sys.Uid, sys.Gid, os.Getuid(), os.Getgid())
	}
}
