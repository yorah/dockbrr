package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"dockbrr/internal/config"
	"dockbrr/internal/version"
)

// systemInfoBody drives the endpoint and decodes its JSON body.
func systemInfoBody(t *testing.T, srv *Server, tok, csrf string) map[string]any {
	t.Helper()
	rec := authedGet(t, srv, "/api/system/info", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/system/info = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestSystemInfoReportsBuildRuntimeAndStorage(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	srv.cfg = config.Config{DataDir: "/config", BindAddr: "0.0.0.0:3625"}
	started := time.Date(2026, 7, 11, 9, 14, 2, 0, time.UTC)
	srv.deps.StartedAt = started
	srv.deps.DockerPinger = fakePinger{}
	srv.deps.DockerVersion = func(context.Context) (string, string, error) {
		return "27.3.1", "1.47", nil
	}

	out := systemInfoBody(t, srv, tok, csrf)

	if out["version"] != version.Version {
		t.Errorf("version = %v, want %q", out["version"], version.Version)
	}
	if out["go_version"] != runtime.Version() {
		t.Errorf("go_version = %v, want %q", out["go_version"], runtime.Version())
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; out["platform"] != want {
		t.Errorf("platform = %v, want %q", out["platform"], want)
	}
	if out["db_path"] != "/config/dockbrr.db" {
		t.Errorf("db_path = %v, want /config/dockbrr.db", out["db_path"])
	}
	if out["bind_addr"] != "0.0.0.0:3625" {
		t.Errorf("bind_addr = %v", out["bind_addr"])
	}
	if out["data_dir"] != "/config" {
		t.Errorf("data_dir = %v", out["data_dir"])
	}
	if out["started_at"] != started.Format(time.RFC3339) {
		t.Errorf("started_at = %v, want %s", out["started_at"], started.Format(time.RFC3339))
	}

	docker, _ := out["docker"].(map[string]any)
	if docker["reachable"] != true || docker["version"] != "27.3.1" || docker["api_version"] != "1.47" {
		t.Errorf("docker = %v", docker)
	}

	auth, _ := out["auth"].(map[string]any)
	if auth["username"] != "admin" || auth["method"] != "password" {
		t.Errorf("auth = %v", auth)
	}

	// Never leak secrets, even by key name.
	for _, k := range []string{"github_token", "password", "password_hash", "session"} {
		if _, ok := out[k]; ok {
			t.Errorf("system info leaks %q", k)
		}
	}
}

// Missing VCS stamps (go run, -buildvcs=false) must degrade to empty strings,
// never a 500. The UI renders a placeholder dash for them.
func TestSystemInfoBuildStampsOptional(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	out := systemInfoBody(t, srv, tok, csrf)
	for _, k := range []string{"commit", "build_date"} {
		if _, ok := out[k]; !ok {
			t.Errorf("field %q missing entirely; want present (possibly empty)", k)
		}
		if _, ok := out[k].(string); !ok {
			t.Errorf("field %q = %v, want string", k, out[k])
		}
	}
	if _, ok := out["commit_dirty"].(bool); !ok {
		t.Errorf("commit_dirty = %v, want bool", out["commit_dirty"])
	}
}

// A nil DockerVersion func (tests), a nil pinger (daemon never came up), and a
// failing ServerVersion must all degrade, never error the request.
func TestSystemInfoDockerDegrades(t *testing.T) {
	cases := map[string]struct {
		pinger  DockerPinger
		version func(context.Context) (string, string, error)
		want    bool
	}{
		"nil pinger": {nil, nil, false},
		"ping fails": {fakePinger{err: errors.New("connection refused")}, func(context.Context) (string, string, error) {
			t.Error("DockerVersion must not be called when Docker is unreachable")
			return "", "", nil
		}, false},
		"nil version func":  {fakePinger{}, nil, true},
		"version func errs": {fakePinger{}, func(context.Context) (string, string, error) { return "27.3.1", "1.47", errors.New("boom") }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _, tok, csrf := authedServer(t, Deps{})
			srv.deps.DockerPinger = tc.pinger
			srv.deps.DockerVersion = tc.version

			out := systemInfoBody(t, srv, tok, csrf)
			docker, _ := out["docker"].(map[string]any)
			if docker["reachable"] != tc.want {
				t.Errorf("reachable = %v, want %v", docker["reachable"], tc.want)
			}
			if _, ok := docker["version"]; ok {
				t.Errorf("version present on degraded docker: %v", docker)
			}
		})
	}
}

// deadlinePinger records the deadline of the context it is probed with, and
// burns a little wall-clock time first so that a handler which opens a SECOND
// context after the ping is observably later.
type deadlinePinger struct {
	seen  *time.Time
	delay time.Duration
}

func (p deadlinePinger) Ping(ctx context.Context) error {
	if d, ok := ctx.Deadline(); ok {
		*p.seen = d
	}
	time.Sleep(p.delay)
	return nil
}

// TestSystemInfoDockerProbesShareOneDeadline pins the contract behind the 4s
// worst case: /api/system/info runs TWO docker probes (ping, then ServerVersion)
// and must derive both from a SINGLE budget created once per request. If each
// probe opens its own 2s context, a wedged (not refused) socket stalls the
// request for 2s + 2s. Asserting that both probes observe the SAME deadline is
// stronger than a wall-clock bound and costs no test time.
func TestSystemInfoDockerProbesShareOneDeadline(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	var pingDL, versionDL time.Time
	srv.deps.DockerPinger = deadlinePinger{seen: &pingDL, delay: 20 * time.Millisecond}
	srv.deps.DockerVersion = func(ctx context.Context) (string, string, error) {
		if d, ok := ctx.Deadline(); ok {
			versionDL = d
		}
		return "27.3.1", "1.47", nil
	}

	start := time.Now()
	out := systemInfoBody(t, srv, tok, csrf)

	docker, _ := out["docker"].(map[string]any)
	if docker["reachable"] != true || docker["version"] != "27.3.1" {
		t.Fatalf("docker = %v, want reachable with version (both probes must run)", docker)
	}
	if pingDL.IsZero() {
		t.Fatal("ping ran on a context with no deadline; docker probes must be bounded")
	}
	if versionDL.IsZero() {
		t.Fatal("DockerVersion ran on a context with no deadline; docker probes must be bounded")
	}
	if !pingDL.Equal(versionDL) {
		t.Errorf("probes ran on different deadlines (ping=%v version=%v, delta=%v); both must come from one shared budget, else worst case is 2x the timeout",
			pingDL, versionDL, versionDL.Sub(pingDL))
	}
	// And that single budget is the docker probe timeout, measured from the top
	// of the request, not from whenever the second probe happened to start.
	if latest := start.Add(dockerProbeTimeout + 250*time.Millisecond); versionDL.After(latest) {
		t.Errorf("probe deadline %v exceeds request start + %v; the endpoint's total docker budget must be %v", versionDL, dockerProbeTimeout, dockerProbeTimeout)
	}
}

func TestSystemInfoRequiresAuth(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/info", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d, want 401", rec.Code)
	}
}
