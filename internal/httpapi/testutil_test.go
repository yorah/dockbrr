package httpapi

import (
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dockbrr/internal/config"
	"dockbrr/internal/store"
)

// authedServer builds a Server over a temp DB with a seeded admin user + live
// session, returning the server, db, the raw session token, and the CSRF token
// for driving authenticated handler tests.
func authedServer(t *testing.T, deps Deps) (*Server, *store.DB, string, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if deps.Users == nil {
		deps.Users = store.NewUsers(db)
	}
	if deps.Sessions == nil {
		deps.Sessions = store.NewSessions(db)
	}
	uid, err := deps.Users.Create("admin", "$argon2id$hash")
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := newToken()
	csrf, _ := newToken()
	if err := deps.Sessions.Create(hashToken(tok), uid, csrf, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return New(config.Config{}, db, deps), db, tok, csrf
}

// authReq attaches the session cookie and (for mutating methods) the CSRF header.
func authReq(req *http.Request, tok, csrf string) *http.Request {
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
	if isMutating(req.Method) {
		req.Header.Set(csrfHeader, csrf)
	}
	return req
}

// mergeDeps copies the non-zero fields of over onto base and returns the
// result. It lets a test re-wire settings/credential/project/service deps onto
// the db that authedServer already created, without a second constructor.
func mergeDeps(base, over Deps) Deps {
	bv := reflect.ValueOf(&base).Elem()
	ov := reflect.ValueOf(over)
	zero := reflect.Zero(ov.Type())
	for i := 0; i < ov.NumField(); i++ {
		f := ov.Field(i)
		if !reflect.DeepEqual(f.Interface(), zero.Field(i).Interface()) {
			bv.Field(i).Set(f)
		}
	}
	return base
}

// pathf is a tiny fmt.Sprintf alias for building request paths in tests.
func pathf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
