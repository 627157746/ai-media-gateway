package fal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

func TestListModelsCatalog(t *testing.T) {
	var gotQ url.Values
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"endpoint_id": "fal-ai/flux/dev", "metadata": map[string]any{"display_name": "FLUX.1 [dev]", "category": "text-to-image", "description": "d", "status": "active"}},
				{"endpoint_id": "fal-ai/v2v", "metadata": map[string]any{"display_name": "V2V", "category": "video-to-video", "status": "active"}},
				{"endpoint_id": "x/tts", "metadata": map[string]any{"category": "text-to-speech", "status": "active"}},
				{"endpoint_id": "y/itov", "metadata": map[string]any{"category": "image-to-video", "status": "active"}},
			},
			"next_cursor": "Mg==",
			"has_more":    true,
		})
	}))
	defer srv.Close()
	defer SetCatalogBase(srv.URL)()

	call := provider.Call{HTTP: srv.Client(), Account: provider.AccountConfig{APIKey: "k-secret"}}
	page, err := Adapter{}.ListModels(context.Background(), call, provider.ModelQuery{Q: "flux", Category: "text-to-image", Cursor: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Key k-secret" {
		t.Fatalf("auth header: %q", gotAuth)
	}
	for _, kv := range [][2]string{
		{"q", "flux"}, {"category", "text-to-image"}, {"cursor", "abc"},
		{"status", "active"}, {"limit", "50"},
	} {
		if gotQ.Get(kv[0]) != kv[1] {
			t.Fatalf("param %s = %q, want %q (query %v)", kv[0], gotQ.Get(kv[0]), kv[1], gotQ)
		}
	}
	if page.NextCursor != "Mg==" {
		t.Fatalf("next_cursor: %q", page.NextCursor)
	}
	if len(page.Models) != 3 { // video-to-video dropped
		t.Fatalf("models: %+v", page.Models)
	}
	m0 := page.Models[0]
	if m0.ID != "fal-ai/flux/dev" || m0.Kind != media.KindImage || m0.Name != "FLUX.1 [dev]" || m0.Description != "d" || m0.Category != "text-to-image" {
		t.Fatalf("model[0]: %+v", m0)
	}
	if page.Models[1].Kind != media.KindSpeech || page.Models[1].Name != "x/tts" { // no display_name → id
		t.Fatalf("model[1]: %+v", page.Models[1])
	}
	if page.Models[2].Kind != media.KindVideo {
		t.Fatalf("model[2]: %+v", page.Models[2])
	}
}

// Find mode does one request per id; 404 means "doesn't exist".
func findModeServer(t *testing.T, known map[string]string, got *[]url.Values, auths *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*got = append(*got, r.URL.Query())
		*auths = append(*auths, r.Header.Get("Authorization"))
		mu.Unlock()
		id := r.URL.Query().Get("endpoint_id")
		if id == "boom/x" {
			w.WriteHeader(500)
			return
		}
		cat, ok := known[id]
		if !ok {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"type": "not_found", "message": "Endpoint(s) not found"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{
				"endpoint_id": id,
				"metadata":    map[string]any{"display_name": "M-" + id, "category": cat, "status": "active"},
			}},
			"next_cursor": nil, "has_more": false,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListModelsFindMode(t *testing.T) {
	known := map[string]string{
		"a/b": "text-to-image",
		"c/d": "text-to-speech",
		"e/f": "video-to-video", // non-importable category → dropped even when found
	}
	var gotQ []url.Values
	var auths []string
	srv := findModeServer(t, known, &gotQ, &auths)
	defer SetCatalogBase(srv.URL)()

	// mixed list → only existing, in input order; e/f dropped by category
	page, err := Adapter{}.ListModels(context.Background(),
		provider.Call{HTTP: srv.Client()},
		provider.ModelQuery{IDs: []string{"a/b", "x/missing", "e/f", "c/d"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 2 || page.Models[0].ID != "a/b" || page.Models[1].ID != "c/d" {
		t.Fatalf("mixed: %+v", page.Models)
	}
	if page.NextCursor != "" {
		t.Fatalf("next_cursor: %q", page.NextCursor)
	}
	// every request carried exactly one endpoint_id and no auth (no key)
	if len(gotQ) != 4 {
		t.Fatalf("requests: %d", len(gotQ))
	}
	for i, q := range gotQ {
		if ids := q["endpoint_id"]; len(ids) != 1 {
			t.Fatalf("request %d multi-id: %v", i, ids)
		}
		if q.Get("q") != "" || q.Get("category") != "" || q.Get("cursor") != "" {
			t.Fatalf("request %d extra params: %v", i, q)
		}
		if auths[i] != "" {
			t.Fatalf("request %d auth without key: %q", i, auths[i])
		}
	}

	// all missing → empty page, no error
	gotQ, auths = nil, nil
	page, err = Adapter{}.ListModels(context.Background(),
		provider.Call{HTTP: srv.Client()},
		provider.ModelQuery{IDs: []string{"x/1", "x/2"}})
	if err != nil || len(page.Models) != 0 {
		t.Fatalf("all missing: %v %+v", err, page)
	}
	if len(gotQ) != 2 {
		t.Fatalf("requests: %d", len(gotQ))
	}
}

func TestListModelsFindModeError(t *testing.T) {
	var gotQ []url.Values
	var auths []string
	srv := findModeServer(t, map[string]string{"a/b": "text-to-image"}, &gotQ, &auths)
	defer SetCatalogBase(srv.URL)()

	// a 500 for one id aborts with an error
	_, err := Adapter{}.ListModels(context.Background(),
		provider.Call{HTTP: srv.Client()},
		provider.ModelQuery{IDs: []string{"a/b", "boom/x", "x/2"}})
	var ue *media.UpstreamError
	if !errors.As(err, &ue) || ue.Status != 500 {
		t.Fatalf("err: %v", err)
	}
}
