// Package compose is dockbrr's compose surface: a whitelisted verb runner that
// shells `docker compose` via exec argv (never a shell string), and a pure-Go
// compose-go parser for read-only service/image preview. Only the Job Engine
// invokes the runner; it is the sole Docker-mutating path.
package compose

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"dockbrr/internal/logger"
)

// ErrVerbNotAllowed is returned when a RunSpec names a verb outside the
// whitelist. It is returned before any process is started.
var ErrVerbNotAllowed = errors.New("compose verb not allowed")

// allowedVerbs is the exhaustive set of compose verbs dockbrr may execute.
var allowedVerbs = map[string]bool{
	"pull": true, "up": true, "down": true, "ps": true, "config": true,
}

// RunSpec is a single compose invocation. Verb must be whitelisted; Args are
// appended verbatim as argv (never shell-interpolated). ProjectName, when set,
// is passed as `-p` so compose targets the SAME project namespace the stack was
// launched under (the com.docker.compose.project label), regardless of the
// directory basename or .env.
type RunSpec struct {
	ConfigFiles []string
	ProjectDir  string
	ProjectName string
	Verb        string
	Args        []string
}

// LogSink receives streamed output lines. stream is "stdout" or "stderr".
// Implementations must be safe for concurrent calls: Write is invoked from
// both the stdout- and stderr-streaming goroutines with no external
// synchronization between them.
type LogSink interface {
	Write(stream, line string)
}

// Runner executes a compose RunSpec.
type Runner interface {
	Run(ctx context.Context, spec RunSpec, sink LogSink) (exitCode int, err error)
}

// buildArgs builds the argv following the program name, enforcing the verb
// whitelist. It never interpolates a shell; every element is a literal arg.
func buildArgs(spec RunSpec) ([]string, error) {
	if !allowedVerbs[spec.Verb] {
		return nil, fmt.Errorf("%w: %q", ErrVerbNotAllowed, spec.Verb)
	}
	args := make([]string, 0, 4+2*len(spec.ConfigFiles)+len(spec.Args))
	args = append(args, "compose")
	for _, f := range spec.ConfigFiles {
		args = append(args, "-f", f)
	}
	if spec.ProjectDir != "" {
		args = append(args, "--project-directory", spec.ProjectDir)
	}
	if spec.ProjectName != "" {
		args = append(args, "-p", spec.ProjectName)
	}
	args = append(args, spec.Verb)
	args = append(args, spec.Args...)
	return args, nil
}

// Preview renders the display-only command string for a spec. It enforces the
// same whitelist so a preview can never show a command the runner would refuse.
func Preview(spec RunSpec) (string, error) {
	args, err := buildArgs(spec)
	if err != nil {
		return "", err
	}
	return "docker " + strings.Join(args, " "), nil
}

// ScopeTargets returns the trailing service selector for pull/up when scope is a
// single service, else nil (whole project).
func ScopeTargets(scope, service string) []string {
	if scope == "service" && service != "" {
		return []string{service}
	}
	return nil
}

// PullSpec builds the `compose pull` spec for a scope. Single source of truth
// shared by the Applier and the /preview endpoint.
func PullSpec(files []string, dir, projectName, scope, service string) RunSpec {
	return RunSpec{ConfigFiles: files, ProjectDir: dir, ProjectName: projectName, Verb: "pull", Args: ScopeTargets(scope, service)}
}

// UpSpec builds the `compose up -d` spec for a scope; service scope adds
// --no-deps <service> to avoid cascading restarts.
func UpSpec(files []string, dir, projectName, scope, service string) RunSpec {
	args := []string{"-d"}
	if scope == "service" && service != "" {
		args = append(args, "--no-deps", service)
	}
	return RunSpec{ConfigFiles: files, ProjectDir: dir, ProjectName: projectName, Verb: "up", Args: args}
}

// ForceRecreateSpec builds a `compose up -d --no-deps --force-recreate
// <services...>` spec that recreates exactly the named services even when
// compose's own config-hash diff would otherwise leave them alone. Used to
// rejoin namespace-sharing dependents (network_mode/ipc/pid: service:<X>,
// see NamespaceDependents) after X itself has just been recreated.
func ForceRecreateSpec(files []string, dir, projectName string, services []string) RunSpec {
	args := append([]string{"-d", "--no-deps", "--force-recreate"}, services...)
	return RunSpec{ConfigFiles: files, ProjectDir: dir, ProjectName: projectName, Verb: "up", Args: args}
}

// ExecRunner runs compose via the real `docker` CLI using exec argv only.
type ExecRunner struct {
	prog string // the program to exec; "docker" in production
}

// NewExecRunner builds a runner that invokes the `docker` CLI on PATH.
func NewExecRunner() *ExecRunner { return &ExecRunner{prog: "docker"} }

// Run executes the compose command, streaming stdout/stderr to sink line by
// line. A process that starts and exits non-zero returns (exitCode, nil); only a
// failure to start/attach returns a non-nil error.
func (r *ExecRunner) Run(ctx context.Context, spec RunSpec, sink LogSink) (int, error) {
	args, err := buildArgs(spec)
	if err != nil {
		return -1, err
	}
	logger.Tracef("compose: exec %s %v", r.prog, args)
	cmd := exec.CommandContext(ctx, r.prog, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return -1, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return -1, err
	}
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go streamPipe(&wg, stdout, "stdout", sink)
	go streamPipe(&wg, stderr, "stderr", sink)
	wg.Wait()

	// Contract: callers MUST check err before interpreting exitCode. Both a
	// clean non-zero exit (exitCode set, err nil) and a start/attach failure
	// (exitCode -1, err set) can flow through here. The exit code alone is
	// not meaningful unless err is nil.
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil // non-zero exit: reported via code, not error
		}
		return -1, err
	}
	return 0, nil
}

// transientProgressRe matches docker's per-layer, per-tick pull progress:
// "<layerid> Downloading 5.243MB", "<layerid> Extracting 1B", etc. docker
// rewrites these in place in a TTY; captured as a stream, each tick becomes a
// separate line, so one apply persists dozens of near-duplicate job_log rows.
// Terminal states ("Download complete", "Pull complete", "Already exists"),
// image/container lifecycle, and all non-pull output do not match and are kept.
var transientProgressRe = regexp.MustCompile(`^\S+\s+(?:Downloading|Extracting|Waiting|Verifying Checksum|Pending|Pulling fs layer)\b`)

// isTransientProgress reports whether a line is interim docker pull progress
// that should be dropped from the job log to keep it readable.
func isTransientProgress(line string) bool {
	return transientProgressRe.MatchString(line)
}

// streamPipe scans a pipe line-by-line into the sink, dropping interim docker
// pull-progress noise. If the scanner stops early due to an error (e.g. a line
// exceeding the buffer limit), that error is surfaced as a line on the same
// stream rather than silently dropping the rest of the stream's output.
func streamPipe(wg *sync.WaitGroup, rc io.Reader, stream string, sink LogSink) {
	defer wg.Done()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if isTransientProgress(line) {
			continue
		}
		sink.Write(stream, line)
	}
	if err := sc.Err(); err != nil {
		sink.Write(stream, "[dockbrr: log stream truncated: "+err.Error()+"]")
	}
}
