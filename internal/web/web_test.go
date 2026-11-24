package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndexServedWithCSP(t *testing.T) {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("csp: %q", w.Header().Get("Content-Security-Policy"))
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
	if !strings.Contains(w.Body.String(), `src="/assets/app.js"`) {
		t.Fatal("index does not load app.js")
	}
	if strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("inline script would violate CSP")
	}
}

func TestAssetContentType(t *testing.T) {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/assets/app.css", nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestUnknownPath404(t *testing.T) {
	for _, p := range []string{"/foo", "/assets/../secret", "/assets/missing.js"} {
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != 404 {
			t.Fatalf("%s → %d", p, w.Code)
		}
	}
}
