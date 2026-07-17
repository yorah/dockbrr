package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
)

const (
	sessionCookie = "dockbrr_session"
	csrfCookie    = "dockbrr_csrf"
	csrfHeader    = "X-CSRF-Token"
	sessionTTL    = 7 * 24 * time.Hour
)

var (
	errUnauthorized   = errors.New("unauthorized")
	errForbidden      = errors.New("forbidden")
	errSetupDone      = errors.New("setup already completed")
	errBadCredentials = errors.New("invalid username or password")
	errInternal       = errors.New("internal server error")
)

type ctxKey int

const userIDKey ctxKey = 0

// userIDFromCtx returns the authenticated user id set by requireAuth.
func userIDFromCtx(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(userIDKey).(int64)
	return id, ok
}

// newToken returns a URL-safe random token with 256 bits of entropy.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken returns hex(sha256(tok)); only this is stored so a DB leak never
// exposes a live cookie.
func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// isSecure reports whether the request arrived over HTTPS (direct TLS or via a
// TLS-terminating proxy), gating the cookie Secure attribute.
func isSecure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// setSessionCookies issues the httpOnly session cookie and the JS-readable CSRF
// cookie.
func setSessionCookies(w http.ResponseWriter, r *http.Request, token, csrf string) {
	secure := isSecure(r)
	maxAge := int(sessionTTL / time.Second)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: csrf, Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

// clearSessionCookies expires both cookies (logout).
func clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := isSecure(r)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: "", Path: "/",
		HttpOnly: false, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// requireAuth authenticates via the session cookie and enforces CSRF
// double-submit on mutating methods. On success the user id is in the context.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeJSONError(w, http.StatusUnauthorized, errUnauthorized)
			return
		}
		sess, err := s.deps.Sessions.Get(hashToken(c.Value), time.Now())
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, errUnauthorized)
			return
		}
		if isMutating(r.Method) {
			hdr := r.Header.Get(csrfHeader)
			if hdr == "" || subtle.ConstantTimeCompare([]byte(hdr), []byte(sess.CSRFToken)) != 1 {
				writeJSONError(w, http.StatusForbidden, errForbidden)
				return
			}
		}
		ctx := context.WithValue(r.Context(), userIDKey, sess.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// cspPolicy is the app's Content-Security-Policy. The SPA is fully
// self-contained (invariant #7: no CDN), so 'self' covers scripts, styles,
// fonts, XHR and SSE. The two relaxations are deliberate: React/Radix set
// inline style ATTRIBUTES ('unsafe-inline' in style-src governs those; inline
// <style> injection is still same-origin only), and Vite inlines small
// images/fonts as data: URIs.
const cspPolicy = "default-src 'self'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self' data:; connect-src 'self'; object-src 'none'; " +
	"base-uri 'self'; frame-ancestors 'none'; form-action 'self'"

// secureHeaders sets baseline hardening headers on every response, API and
// SPA alike. Registered first on the mux so chi applies it to NotFound (the
// SPA fallback) too.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", cspPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
