package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"dockbrr/internal/auth"
	"dockbrr/internal/store"
)

// dummyHash is verified against on unknown-user logins so the response time does
// not reveal whether the username exists (blunts the enumeration oracle).
var dummyHash, _ = auth.HashPassword("dockbrr-dummy-password")

type credsBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetupStatus reports whether first-run setup is still required.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.deps.Users.Count()
	if err != nil {
		writeInternalError(w, "setup status: count users", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": n == 0})
}

// handleSetup creates the admin user on first run and logs them in. It
// self-disables once any user exists.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	n, err := s.deps.Users.Count()
	if err != nil {
		writeInternalError(w, "setup: count users", err)
		return
	}
	if n > 0 {
		writeJSONError(w, http.StatusConflict, errSetupDone)
		return
	}
	var body credsBody
	if err := decodeJSON(r, &body); err != nil || body.Username == "" || body.Password == "" {
		writeJSONError(w, http.StatusBadRequest, errBadCredentials)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeInternalError(w, "setup: hash password", err)
		return
	}
	uid, err := s.deps.Users.Create(body.Username, hash)
	if err != nil {
		writeInternalError(w, "setup: create user", err)
		return
	}
	if err := s.issueSession(w, r, uid); err != nil {
		writeInternalError(w, "setup: issue session", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": body.Username})
}

// handleLogin verifies credentials and issues a session. Failed attempts feed
// the per-IP lockout; a locked-out IP gets 429 before any credential work.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retry, blocked := s.limiter.blocked(ip); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeJSONError(w, http.StatusTooManyRequests, errTooManyAttempts)
		return
	}
	var body credsBody
	if err := decodeJSON(r, &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, errBadCredentials)
		return
	}
	u, err := s.deps.Users.GetByUsername(body.Username)
	if err != nil {
		_, _ = auth.VerifyPassword(dummyHash, body.Password) // constant-ish time
		s.limiter.fail(ip)
		writeJSONError(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, body.Password)
	if err != nil || !ok {
		s.limiter.fail(ip)
		writeJSONError(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	s.limiter.success(ip)
	if err := s.issueSession(w, r, u.ID); err != nil {
		writeInternalError(w, "login: issue session", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username})
}

// handleLogout deletes the current session and clears cookies.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.deps.Sessions.Delete(hashToken(c.Value))
	}
	clearSessionCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleMe returns the authenticated user's username.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromCtx(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	// Single-user v1: fetch by scanning is avoided; look up via a small helper.
	u, err := s.userByID(uid)
	if err != nil {
		writeInternalError(w, "me: get user", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username})
}

// issueSession generates a token + csrf, persists the session, and sets cookies.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	tok, err := newToken()
	if err != nil {
		return err
	}
	csrf, err := newToken()
	if err != nil {
		return err
	}
	if err := s.deps.Sessions.Create(hashToken(tok), userID, csrf, time.Now().Add(sessionTTL)); err != nil {
		return err
	}
	setSessionCookies(w, r, tok, csrf)
	return nil
}

// userByID looks up a user by id (single-user v1; small linear helper via
// GetByUsername is not available, so add a store method OR scan). See note.
func (s *Server) userByID(id int64) (store.User, error) {
	return s.deps.Users.GetByID(id)
}

func timeNow() time.Time { return time.Now() }
