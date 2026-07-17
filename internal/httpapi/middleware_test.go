package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dockbrr/internal/config"
	"dockbrr/internal/store"
)

// newMiddlewareServer builds a Server over a temp DB for exercising requireAuth
// directly on an ad-hoc handler (no route mounting needed).
func newMiddlewareServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	deps := Deps{Sessions: store.NewSessions(db), Users: store.NewUsers(db)}
	s := New(config.Config{}, db, deps)
	return s, db
}

func TestRequireAuthRejectsNoCookie(t *testing.T) {
	s, _ := newMiddlewareServer(t)
	h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-cookie status = %d, want 401", rec.Code)
	}
}

func TestRequireAuthAcceptsValidSession(t *testing.T) {
	s, db := newMiddlewareServer(t)
	uid, _ := store.NewUsers(db).Create("admin", "h")
	tok, _ := newToken()
	_ = store.NewSessions(db).Create(hashToken(tok), uid, "csrf-abc", time.Now().Add(time.Hour))

	h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := userIDFromCtx(r.Context())
		if !ok || id != uid {
			t.Errorf("ctx user id = %d ok=%v, want %d", id, ok, uid)
		}
		w.WriteHeader(200)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("valid session status = %d, want 200", rec.Code)
	}
}

func TestRequireAuthRejectsMutatingWithoutCSRF(t *testing.T) {
	s, db := newMiddlewareServer(t)
	uid, _ := store.NewUsers(db).Create("admin", "h")
	tok, _ := newToken()
	_ = store.NewSessions(db).Create(hashToken(tok), uid, "csrf-abc", time.Now().Add(time.Hour))

	h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	// POST without the header -> 403.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF = %d, want 403", rec.Code)
	}

	// POST with a wrong header -> 403.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	req.Header.Set(csrfHeader, "wrong")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST wrong CSRF = %d, want 403", rec.Code)
	}

	// POST with the matching header -> 200.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	req.Header.Set(csrfHeader, "csrf-abc")
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("POST matching CSRF = %d, want 200", rec.Code)
	}
}

// TestSecureHeaders asserts the hardening headers are present on an API route
// and on the SPA fallback (chi routes NotFound through the middleware stack too).
func TestSecureHeaders(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	for _, path := range []string{"/healthz", "/", "/api/setup/status"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)

		h := rec.Header()
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
		if got := h.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "same-origin" {
			t.Errorf("%s: Referrer-Policy = %q, want same-origin", path, got)
		}
		csp := h.Get("Content-Security-Policy")
		for _, directive := range []string{
			"default-src 'self'",
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data:",
			"font-src 'self' data:",
			"object-src 'none'",
			"frame-ancestors 'none'",
			"base-uri 'self'",
		} {
			if !strings.Contains(csp, directive) {
				t.Errorf("%s: CSP missing %q (got %q)", path, directive, csp)
			}
		}
	}
}

func TestRequireAuthRejectsExpiredSession(t *testing.T) {
	s, db := newMiddlewareServer(t)
	uid, _ := store.NewUsers(db).Create("admin", "h")
	tok, _ := newToken()
	_ = store.NewSessions(db).Create(hashToken(tok), uid, "csrf", time.Now().Add(-time.Minute))
	h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired session = %d, want 401", rec.Code)
	}
}
