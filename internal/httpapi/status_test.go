package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"dockbrr/internal/secret"
	"dockbrr/internal/store"
)

// fakePinger stands in for *docker.Client: Ping returns err verbatim so tests
// can drive both the reachable and unreachable branches of /api/status.
type fakePinger struct{ err error }

func (p fakePinger) Ping(context.Context) error { return p.err }

func statusDeps(t *testing.T, db *store.DB, pinger DockerPinger) Deps {
	t.Helper()
	key, _ := secret.LoadOrCreateKey(t.TempDir())
	sealer, _ := secret.NewSealer(key)
	return Deps{
		Sealer:       sealer,
		Settings:     store.NewSettings(db, sealer),
		DockerPinger: pinger,
	}
}

func TestStatusEndpoint(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, statusDeps(t, db, fakePinger{}))

	if err := srv.deps.Settings.Set("last_check_all", "2026-07-04T10:00:00Z"); err != nil {
		t.Fatal(err)
	}

	rec := authedGet(t, srv, "/api/status", tok, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["last_check_all"] != "2026-07-04T10:00:00Z" {
		t.Errorf("last_check_all: %v", out)
	}
	if out["docker_reachable"] != true {
		t.Errorf("docker_reachable: %v", out)
	}
}

// docker_reachable is a live per-request probe: a failing Ping (daemon down) and
// a nil pinger (daemon never came up) must both report false.
func TestStatusDockerUnreachable(t *testing.T) {
	cases := map[string]DockerPinger{
		"ping fails": fakePinger{err: errors.New("dial unix: connection refused")},
		"nil pinger": nil,
	}
	for name, pinger := range cases {
		t.Run(name, func(t *testing.T) {
			srv, db, tok, csrf := authedServer(t, Deps{})
			srv.deps = mergeDeps(srv.deps, statusDeps(t, db, pinger))

			rec := authedGet(t, srv, "/api/status", tok, csrf)
			if rec.Code != http.StatusOK {
				t.Fatalf("want 200, got %d", rec.Code)
			}
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if out["docker_reachable"] != false {
				t.Errorf("docker_reachable: want false, got %v", out["docker_reachable"])
			}
		})
	}
}

// The status endpoint sits behind requireAuth; an unauthenticated request must
// be rejected with 401 rather than leaking scheduler/daemon state.
func TestStatusRequiresAuth(t *testing.T) {
	srv, db, _, _ := authedServer(t, Deps{})
	srv.deps = mergeDeps(srv.deps, statusDeps(t, db, fakePinger{}))

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}
