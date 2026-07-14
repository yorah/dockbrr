package httpapi

import (
	"errors"
	"net/http"

	"dockbrr/internal/auth"
)

// handleChangePassword verifies the current password, stores a new argon2id
// hash, revokes ALL sessions for the user, then re-issues a session so the
// current browser stays logged in. Revoke-all means every other device is
// logged out on a password change.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	uid, ok := userIDFromCtx(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, errUnauthorized)
		return
	}
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decodeJSON(r, &body); err != nil || body.New == "" {
		writeJSONError(w, http.StatusBadRequest, errors.New("current and new passwords are required"))
		return
	}
	u, err := s.deps.Users.GetByID(uid)
	if err != nil {
		writeInternalError(w, "change password: get user", err)
		return
	}
	okPw, err := auth.VerifyPassword(u.PasswordHash, body.Current)
	if err != nil || !okPw {
		writeJSONError(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	hash, err := auth.HashPassword(body.New)
	if err != nil {
		writeInternalError(w, "change password: hash", err)
		return
	}
	if err := s.deps.Users.UpdatePasswordHash(uid, hash); err != nil {
		writeInternalError(w, "change password: update hash", err)
		return
	}
	// Revoke every existing session (incl. the current one), then re-issue.
	if err := s.deps.Sessions.DeleteByUser(uid); err != nil {
		writeInternalError(w, "change password: revoke sessions", err)
		return
	}
	if err := s.issueSession(w, r, uid); err != nil {
		writeInternalError(w, "change password: issue session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
