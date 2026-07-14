package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dockbrr/internal/auth"
)

// seedRealAdmin replaces the placeholder hash authedServer seeds with a real
// argon2id hash of `pw`, so password-change can verify the current password.
func seedRealAdmin(t *testing.T, srv *Server, pw string) {
	t.Helper()
	h, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	u, err := srv.deps.Users.GetByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.deps.Users.UpdatePasswordHash(u.ID, h); err != nil {
		t.Fatal(err)
	}
}

func TestChangePasswordSuccess(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	seedRealAdmin(t, srv, "old-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/password",
		strings.NewReader(`{"current":"old-secret","new":"new-secret"}`))
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	// The new password verifies against the stored hash.
	u, _ := srv.deps.Users.GetByUsername("admin")
	ok, _ := auth.VerifyPassword(u.PasswordHash, "new-secret")
	if !ok {
		t.Fatal("new password does not verify after change")
	}
	// A fresh, working session cookie was issued (re-login the current browser).
	// Assert the VALUE and attributes, not just the cookie name: a logout
	// (deletion) cookie carries the same name with an empty value / MaxAge<=0.
	var sc *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			sc = c
		}
	}
	if sc == nil {
		t.Fatal("expected a session cookie after password change")
	}
	if sc.Value == "" || sc.MaxAge <= 0 {
		t.Fatalf("expected a fresh (non-deletion) session cookie, got value=%q maxAge=%d", sc.Value, sc.MaxAge)
	}
	// And it must resolve to a live session for the user (proves it's a real
	// re-login, not a stale/logout cookie).
	if _, err := srv.deps.Sessions.Get(hashToken(sc.Value), time.Now()); err != nil {
		t.Fatalf("fresh session cookie does not resolve to a live session: %v", err)
	}
}

// TestChangePasswordRevokesOtherSessions exercises the security-defining
// revoke-all behavior: a second device's session must be invalidated by a
// password change.
func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	seedRealAdmin(t, srv, "old-secret")

	u, err := srv.deps.Users.GetByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a second logged-in device: a separate session for the same user.
	otherTok := "second-device-token"
	if err := srv.deps.Sessions.Create(hashToken(otherTok), u.ID, "other-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.deps.Sessions.Get(hashToken(otherTok), time.Now()); err != nil {
		t.Fatalf("precondition: second session should be valid before change: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/password",
		strings.NewReader(`{"current":"old-secret","new":"new-secret"}`))
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	// The other device's session must now be gone.
	if _, err := srv.deps.Sessions.Get(hashToken(otherTok), time.Now()); err == nil {
		t.Fatal("expected second-device session to be revoked after password change")
	}
}

func TestChangePasswordWrongCurrent(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	seedRealAdmin(t, srv, "old-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/password",
		strings.NewReader(`{"current":"WRONG","new":"new-secret"}`))
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestChangePasswordEmptyNew(t *testing.T) {
	srv, _, tok, csrf := authedServer(t, Deps{})
	seedRealAdmin(t, srv, "old-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/password",
		strings.NewReader(`{"current":"old-secret","new":""}`))
	req = authReq(req, tok, csrf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestChangePasswordUnauthenticated(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password",
		strings.NewReader(`{"current":"x","new":"y"}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}
