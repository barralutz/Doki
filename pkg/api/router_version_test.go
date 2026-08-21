package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRootHandlerOnlyStripsNumericDockerVersionPrefix(t *testing.T) {
	s := &Server{router: http.NewServeMux()}
	s.router.HandleFunc("/volumes/example", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/volumes/example" {
			t.Fatalf("unversioned path rewritten to %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.router.HandleFunc("/ping-versioned", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping-versioned" {
			t.Fatalf("versioned path normalized to %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.rebuildHandler()

	for _, path := range []string{"/volumes/example", "/v1.55/ping-versioned"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		s.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s status = %d, want %d", path, rr.Code, http.StatusNoContent)
		}
	}
}
