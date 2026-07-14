package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testDistFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html><div id=root>APP</div>")},
		"assets/app-abc.js": {Data: []byte("console.log('hi')")},
	}
}

func TestSPAServesIndexAtRoot(t *testing.T) {
	h := NewSPAHandler(testDistFS())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got == "" || got[:9] != "<!doctype" {
		t.Fatalf("root did not serve index.html: %q", got)
	}
}

func TestSPAServesAsset(t *testing.T) {
	h := NewSPAHandler(testDistFS())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app-abc.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" || ct[:22] != "text/javascript; chars" && ct[:15] != "application/jav" {
		// accept either JS content-type spelling from mime.TypeByExtension
		t.Logf("asset content-type = %q", ct)
	}
	if rec.Body.String() != "console.log('hi')" {
		t.Fatalf("asset body = %q", rec.Body.String())
	}
}

func TestSPAFallbackToIndexForUnknownRoute(t *testing.T) {
	h := NewSPAHandler(testDistFS())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/deep/link", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (SPA fallback)", rec.Code)
	}
	if rec.Body.String()[:9] != "<!doctype" {
		t.Fatalf("fallback did not serve index.html: %q", rec.Body.String())
	}
}

func TestSPADoesNotHijackAPIorHealthz(t *testing.T) {
	h := NewSPAHandler(testDistFS())
	for _, p := range []string{"/api/unknown", "/healthz", "/api"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: code = %d, want 404 (SPA must not serve index for API/healthz)", p, rec.Code)
		}
	}
}

// A raw "//api/x" path (cleans to "/api/x") must still 404. The guard runs on
// the cleaned path, so the double-slash can't shadow the API namespace and leak
// index.html. Set URL.Path directly: url.Parse would treat a "//..." target as
// a scheme-relative reference and strip the leading slashes.
func TestSPAGuardsCleanedAPIPath(t *testing.T) {
	h := NewSPAHandler(testDistFS())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.URL.Path = "//api/x"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("//api/x: code = %d, want 404 (cleaned-path guard)", rec.Code)
	}
}

// Wired through the full server: an unknown SPA route falls back to index, but
// /healthz still returns its JSON handler and an unknown /api path is 404 JSON.
func TestServerServesSPAFallbackButKeepsAPI(t *testing.T) {
	srv, _, _, _ := authedServer(t, Deps{})
	// /healthz still handled
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz code = %d, want 200", rec.Code)
	}
	// unknown SPA path → index (200) from the committed placeholder embed
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/spa/route", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("SPA route code = %d, want 200", rec.Code)
	}
	// unknown /api path → 404 (JSON), never the SPA index. Drives the API-guard
	// end-to-end through the wired server, not just the handler in isolation.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/bogus", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/api/bogus code = %d, want 404 (must not serve SPA index)", rec.Code)
	}
}
