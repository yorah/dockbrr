package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsTransientProgress(t *testing.T) {
	t.Parallel()
	// Drop all per-layer pull output: both the per-tick interim states docker
	// rewrites in place in a TTY, and the per-layer terminal states (one per
	// layer, still dozens of rows on a many-layer image). Keep image-level
	// pull lifecycle + container lifecycle + non-pull output.
	drop := []string{
		"12cf9316ae87 Downloading 5.243MB",
		"f6e607ad0f52 Downloading 1.103kB",
		"f6e607ad0f52 Extracting 1B",
		"3b3d036990fd Waiting 0B",
		"5457d5da3ec8 Verifying Checksum 0B",
		"45983244b0aa Pending 0B",
		"3b3d036990fd Pulling fs layer 0B",
		// Per-layer terminal states: one line per layer, dropped so the log
		// collapses to the image-level Pulling -> Pulled pair.
		"4f4fb700ef54 Already exists 0B",
		"f6e607ad0f52 Download complete 0B",
		"4f4fb700ef54 Pull complete 0B",
		// docker compose indents progress lines with leading whitespace; these
		// are verbatim captures from a real compose pull.
		" 07683a18a1c6 Pulling fs layer 0B",
		" 575d46df4705 Downloading 1.049MB",
		"  f6e607ad0f52 Extracting 1B",
		" 7e0b8e884178 Download complete 0B",
	}
	keep := []string{
		"Image redis:8.8@sha256:2838 Pulling",
		"Image redis:8.8@sha256:2838 Pulled",
		"Container smoke-cache Recreate",
		"Container smoke-cache Started",
		"apply succeeded",
		"",
		// Indented image-level lifecycle must survive.
		" Image louislam/uptime-kuma:2.4.0 Pulling ",
	}
	for _, l := range drop {
		if !isTransientProgress(l) {
			t.Errorf("expected DROP, kept: %q", l)
		}
	}
	for _, l := range keep {
		if isTransientProgress(l) {
			t.Errorf("expected KEEP, dropped: %q", l)
		}
	}
}

func TestBuildArgsWhitelistRejectsNonWhitelisted(t *testing.T) {
	for _, verb := range []string{"rm", "exec", "run", "kill", "up; rm -rf /", ""} {
		if _, err := buildArgs(RunSpec{Verb: verb}); !errors.Is(err, ErrVerbNotAllowed) {
			t.Fatalf("verb %q: err = %v, want ErrVerbNotAllowed", verb, err)
		}
	}
}

func TestBuildArgsAllowsWhitelistedVerbs(t *testing.T) {
	for _, verb := range []string{"pull", "up", "down", "ps", "config"} {
		if _, err := buildArgs(RunSpec{Verb: verb}); err != nil {
			t.Fatalf("verb %q rejected: %v", verb, err)
		}
	}
}

func TestBuildArgsShape(t *testing.T) {
	got, err := buildArgs(RunSpec{
		ConfigFiles: []string{"/x/compose.yml", "/x/override.yml"},
		ProjectDir:  "/x",
		Verb:        "up",
		Args:        []string{"-d", "--no-deps", "web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"compose", "-f", "/x/compose.yml", "-f", "/x/override.yml",
		"--project-directory", "/x", "up", "-d", "--no-deps", "web",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}

// TestBuildArgsInjectionStaysOneArgv proves user-controlled input (a malicious
// service name) is carried as a single argv element and never split or
// interpreted: the "UI input never becomes a shell argument" invariant.
func TestBuildArgsInjectionStaysOneArgv(t *testing.T) {
	nasty := "web; rm -rf / #"
	got, err := buildArgs(RunSpec{Verb: "up", Args: []string{"-d", nasty}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range got {
		if a == nasty {
			found = true
		}
		if strings.Contains(a, "rm -rf") && a != nasty {
			t.Fatalf("argument was split/mangled: %q", a)
		}
	}
	if !found {
		t.Fatalf("nasty arg not present verbatim as one element: %v", got)
	}
}

func TestPreview(t *testing.T) {
	got, err := Preview(RunSpec{
		ConfigFiles: []string{"/x/compose.yml"}, ProjectDir: "/x",
		Verb: "pull", Args: []string{"web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "docker compose -f /x/compose.yml --project-directory /x pull web"
	if got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

func TestPreviewRejectsNonWhitelisted(t *testing.T) {
	if _, err := Preview(RunSpec{Verb: "exec"}); !errors.Is(err, ErrVerbNotAllowed) {
		t.Fatalf("err = %v, want ErrVerbNotAllowed", err)
	}
}

// recordSink collects streamed lines for assertions.
type recordSink struct{ lines []string }

func (s *recordSink) Write(stream, line string) { s.lines = append(s.lines, stream+":"+line) }

// TestExecRunnerArgvOnlyNoShell points the runner at a fake "docker" script that
// records the exact argv it received, proving exec passes argv directly (a nasty
// service name arrives intact, unsplit, uninterpreted, no shell involved).
func TestExecRunnerArgvOnlyNoShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake docker is POSIX-only")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + out + "\ndone\nexit 0\n"
	fakeDocker := filepath.Join(dir, "docker")
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &ExecRunner{prog: fakeDocker}
	nasty := "web; rm -rf / #"
	code, err := r.Run(context.Background(), RunSpec{
		ConfigFiles: []string{"/x/compose.yml"}, ProjectDir: "/x",
		Verb: "up", Args: []string{"-d", nasty},
	}, &recordSink{})
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	want := []string{"compose", "-f", "/x/compose.yml", "--project-directory", "/x", "up", "-d", nasty}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("received argv = %v, want %v", got, want)
	}
}

// TestExecRunnerNonZeroExitNoError: a clean process exiting non-zero returns the
// code with a nil error (the caller treats code!=0 as failure).
func TestExecRunnerNonZeroExitNoError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fake")
	}
	dir := t.TempDir()
	fakeDocker := filepath.Join(dir, "docker")
	if err := os.WriteFile(fakeDocker, []byte("#!/bin/sh\necho boom 1>&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &ExecRunner{prog: fakeDocker}
	sink := &recordSink{}
	code, err := r.Run(context.Background(), RunSpec{Verb: "pull"}, sink)
	if err != nil {
		t.Fatalf("unexpected err = %v (non-zero exit must not be a Go error)", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	joined := strings.Join(sink.lines, "|")
	if !strings.Contains(joined, "stderr:boom") {
		t.Fatalf("stderr not streamed: %q", joined)
	}
}

// TestExecRunnerStreamTruncationSurfaced proves that a stdout line exceeding
// the scanner's 1MB buffer does not vanish silently: streamPipe must surface
// a truncation marker on the same stream instead of the goroutine just
// returning with no signal (Important 1 review finding).
func TestExecRunnerStreamTruncationSurfaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fake")
	}
	dir := t.TempDir()
	fakeDocker := filepath.Join(dir, "docker")
	// Emit a single line >1MB on stdout with NO trailing newline, so the
	// scanner's buffer limit is exceeded before a token boundary is found.
	// (Built via dd|tr rather than a shell-expanded arg list, which would
	// blow past ARG_MAX for a million-plus positional args.)
	script := "#!/bin/sh\ndd if=/dev/zero bs=1100000 count=1 2>/dev/null | tr '\\0' 'x'\nexit 0\n"
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &ExecRunner{prog: fakeDocker}
	sink := &recordSink{}
	code, err := r.Run(context.Background(), RunSpec{Verb: "pull"}, sink)
	if err != nil {
		t.Fatalf("unexpected err = %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	joined := strings.Join(sink.lines, "|")
	if !strings.Contains(joined, "stdout:[dockbrr: log stream truncated:") {
		t.Fatalf("truncation marker not streamed (silent drop): %q", joined)
	}
}

func TestPullSpecServiceScope(t *testing.T) {
	spec := PullSpec([]string{"docker-compose.yml"}, "/srv/app", "", "service", "web")
	if spec.Verb != "pull" {
		t.Fatalf("verb = %q", spec.Verb)
	}
	if len(spec.Args) != 1 || spec.Args[0] != "web" {
		t.Fatalf("args = %v, want [web]", spec.Args)
	}
}

func TestPullSpecProjectScope(t *testing.T) {
	// Project scope pulls the whole project, no trailing service selector.
	spec := PullSpec([]string{"docker-compose.yml"}, "/srv/app", "", "project", "web")
	if spec.Verb != "pull" {
		t.Fatalf("verb = %q", spec.Verb)
	}
	if len(spec.Args) != 0 {
		t.Fatalf("project-scope pull args = %v, want [] (no service selector)", spec.Args)
	}
}

func TestUpSpecServiceScope(t *testing.T) {
	spec := UpSpec([]string{"docker-compose.yml"}, "/srv/app", "", "service", "web")
	if spec.Verb != "up" {
		t.Fatalf("verb = %q", spec.Verb)
	}
	want := []string{"-d", "--no-deps", "web"}
	if len(spec.Args) != 3 || spec.Args[0] != want[0] || spec.Args[1] != want[1] || spec.Args[2] != want[2] {
		t.Fatalf("args = %v, want %v", spec.Args, want)
	}
}

func TestUpSpecProjectScope(t *testing.T) {
	spec := UpSpec([]string{"docker-compose.yml"}, "/srv/app", "", "project", "web")
	if len(spec.Args) != 1 || spec.Args[0] != "-d" {
		t.Fatalf("project-scope up args = %v, want [-d]", spec.Args)
	}
}

func TestForceRecreateSpec(t *testing.T) {
	spec := ForceRecreateSpec([]string{"docker-compose.yml"}, "/srv/app", "proj", []string{"qbit", "sabnzbd"})
	if spec.Verb != "up" {
		t.Fatalf("verb = %q, want up", spec.Verb)
	}
	if spec.ProjectName != "proj" {
		t.Fatalf("project name = %q, dropped", spec.ProjectName)
	}
	want := []string{"-d", "--no-deps", "--force-recreate", "qbit", "sabnzbd"}
	if strings.Join(spec.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", spec.Args, want)
	}
}
