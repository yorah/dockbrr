package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	t.Cleanup(func() { db.Close() })
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
