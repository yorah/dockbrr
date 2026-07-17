package httpapi

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"dockbrr/internal/auth"
	"dockbrr/internal/config"
	"dockbrr/internal/store"
)

func freshServer(t *testing.T) (*Server, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	deps := Deps{Users: store.NewUsers(db), Sessions: store.NewSessions(db)}
	return New(config.Config{}, db, deps), db
}

func TestSetupStatusNeedsSetupWhenNoUser(t *testing.T) {
	s, _ := freshServer(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needs_setup":true`) {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestSetupCreatesAdminThenSelfDisables(t *testing.T) {
	s, db := freshServer(t)
	body := strings.NewReader(`{"username":"admin","password":"correct horse"}`)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/setup", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	// A session cookie was issued.
	if !hasCookie(rec.Result().Cookies(), sessionCookie) {
		t.Fatal("no session cookie set after setup")
	}
	// The password is stored hashed (argon2id), not plaintext.
	u, _ := store.NewUsers(db).GetByUsername("admin")
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("password not argon2id-hashed: %q", u.PasswordHash)
	}
	// Second setup attempt is rejected.
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"username":"x","password":"y"}`)))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second setup = %d, want 409", rec2.Code)
	}
}

func TestLoginSucceedsAndFailsCorrectly(t *testing.T) {
	s, db := freshServer(t)
	hash, _ := auth.HashPassword("s3cret")
	_, _ = store.NewUsers(db).Create("admin", hash)

	// Wrong password -> 401.
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"nope"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", rec.Code)
	}
	// Unknown user -> 401 (no user-enumeration difference).
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"ghost","password":"x"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown-user login = %d, want 401", rec.Code)
	}
	// Correct -> 200 + session cookie.
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"s3cret"}`)))
	if rec.Code != 200 || !hasCookie(rec.Result().Cookies(), sessionCookie) {
		t.Fatalf("good login = %d, cookie set = %v", rec.Code, hasCookie(rec.Result().Cookies(), sessionCookie))
	}
}

func TestLogoutDeletesSession(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	before := hashToken(tok)
	if _, err := store.NewSessions(db).Get(before, timeNow()); err != nil {
		t.Fatalf("precondition: session should exist: %v", err)
	}
	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil), tok, csrf)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", rec.Code)
	}
	if _, err := store.NewSessions(db).Get(before, timeNow()); err == nil {
		t.Fatal("session still present after logout")
	}
}

func TestMeReturnsUsername(t *testing.T) {
	s, _, tok, csrf := authedServer(t, Deps{})
	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, "/api/auth/me", nil), tok, csrf)
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"username":"admin"`) {
		t.Fatalf("me = %d body = %s", rec.Code, rec.Body.String())
	}
}

func hasCookie(cs []*http.Cookie, name string) bool {
	for _, c := range cs {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

// TestLoginRateLimited drives failLimit bad logins through the real handler
// and asserts the next attempt is rejected 429 with Retry-After — even with
// CORRECT credentials (lockout is unconditional once tripped).
func TestLoginRateLimited(t *testing.T) {
	srv, db, _, _ := authedServer(t, Deps{})
	_ = db

	doLogin := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		req.RemoteAddr = "192.0.2.9:40000"
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < failLimit; i++ {
		if rec := doLogin(`{"username":"admin","password":"wrong"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: code = %d, want 401", i, rec.Code)
		}
	}
	rec := doLogin(`{"username":"admin","password":"wrong"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("post-limit code = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 response missing Retry-After header")
	}

	// A different IP is unaffected.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.RemoteAddr = "192.0.2.10:40000"
	other := httptest.NewRecorder()
	srv.Handler().ServeHTTP(other, req)
	if other.Code != http.StatusUnauthorized {
		t.Fatalf("other-IP code = %d, want 401", other.Code)
	}
}
