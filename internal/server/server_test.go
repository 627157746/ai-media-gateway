package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/admin"
	"ai-media-gateway/internal/config"
	"ai-media-gateway/internal/netproxy"
	"ai-media-gateway/internal/openai"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/server"
	"ai-media-gateway/internal/store"
	"ai-media-gateway/internal/web"
)

type env struct {
	t   *testing.T
	st  *store.Store
	srv http.Handler
	key string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		AdminToken:   "adm-token",
		MaxBodyBytes: 1 << 20,
		CORSOrigins:  []string{"*"},
	}
	_, plain, err := st.CreateAPIKey(context.Background(), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	box := secret.New("mk")
	pool := accounts.New(st, box, config.Cooldowns{}, netproxy.NewClients(0), nil)
	return &env{
		t:  t,
		st: st,
		srv: server.New(server.Deps{
			Config: cfg, Store: st, Pool: pool,
			Admin: admin.New(st, box, netproxy.NewClients(0)), OpenAI: openai.New(st, pool), Web: web.Handler(),
		}),
		key: plain,
	}
}

func (e *env) do(method, path, body, token string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func TestCORSPreflight(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	req.Header.Set("Origin", "https://canvas.local")
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d", rec.Code)
	}
	h := rec.Header()
	if h.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("ACAO: %q", h.Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
		!strings.Contains(h.Get("Access-Control-Expose-Headers"), "Content-Type") {
		t.Fatalf("headers: %v", h)
	}
}

func TestHealthzNoAuth(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/healthz", "", "")
	if rec.Code != 200 {
		t.Fatalf("healthz: %d", rec.Code)
	}
}

func TestV1Auth(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/v1/models", "", ""); rec.Code != 401 {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := e.do("GET", "/v1/models", "", "sk-mg-wrong"); rec.Code != 401 {
		t.Fatalf("bad key: %d", rec.Code)
	}
	// the admin token is accepted too (the built-in 试用 page uses it)
	if rec := e.do("GET", "/v1/models", "", "adm-token"); rec.Code != 200 {
		t.Fatalf("admin token: %d body=%s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/models", "", "adm-token-x"); rec.Code != 401 {
		t.Fatalf("near-admin token: %d", rec.Code)
	}
	rec := e.do("GET", "/v1/models", "", e.key)
	if rec.Code != 200 {
		t.Fatalf("good key: %d body=%s", rec.Code, rec.Body)
	}
	var out struct {
		Object string `json:"object"`
		Data   []any  `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "list" {
		t.Fatalf("shape: %v", out)
	}
	// error shape check
	rec = e.do("GET", "/v1/models", "", "")
	var errOut struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errOut); err != nil || errOut.Error.Message == "" {
		t.Fatalf("error shape: %s", rec.Body)
	}
}

func TestAdminAuth(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/admin/providers", "", ""); rec.Code != 401 {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/providers", "", "wrong"); rec.Code != 401 {
		t.Fatalf("bad token: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/providers", "", "adm-token"); rec.Code != 200 {
		t.Fatalf("good token: %d", rec.Code)
	}
	// gateway key must not work for admin
	if rec := e.do("GET", "/admin/providers", "", e.key); rec.Code != 401 {
		t.Fatalf("gateway key as admin: %d", rec.Code)
	}
}

func TestV1ModelsListing(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.st.CreateProvider(ctx, &store.Provider{ID: "p1", Type: "t", Enabled: true})
	e.st.CreateProvider(ctx, &store.Provider{ID: "p2", Type: "t", Enabled: false})
	e.st.CreateModel(ctx, &store.Model{ID: "m-img", ProviderID: "p1", Kind: "image", Upstream: "u", Enabled: true})
	e.st.CreateModel(ctx, &store.Model{ID: "m-off", ProviderID: "p1", Kind: "video", Upstream: "u", Enabled: false})
	e.st.CreateModel(ctx, &store.Model{ID: "m-p2", ProviderID: "p2", Kind: "speech", Upstream: "u", Enabled: true})

	rec := e.do("GET", "/v1/models", "", e.key)
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
			Kind    string `json:"kind"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 1 {
		t.Fatalf("models: %v", out.Data)
	}
	m := out.Data[0]
	if m.ID != "m-img" || m.Object != "model" || m.OwnedBy != "p1" || m.Kind != "image" {
		t.Fatalf("entry: %+v", m)
	}
}

func TestWebUINoAuth(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/", "", ""); rec.Code != 200 {
		t.Fatalf("/: %d", rec.Code)
	}
	if rec := e.do("GET", "/assets/app.js", "", ""); rec.Code != 200 {
		t.Fatalf("/assets/app.js: %d", rec.Code)
	}
	// UI is public, but API subtrees still enforce auth
	if rec := e.do("GET", "/v1/models", "", ""); rec.Code != 401 {
		t.Fatalf("/v1: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/status", "", ""); rec.Code != 401 {
		t.Fatalf("/admin: %d", rec.Code)
	}
	if rec := e.do("GET", "/no-such-page", "", ""); rec.Code != 404 {
		t.Fatalf("unknown: %d", rec.Code)
	}
}

// request_logs records only model-generation calls: GET /v1/models must not
// create a row, while POST /v1/images/generations does (even when it fails).
func TestRequestLogOnlyModelCalls(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("GET", "/v1/models", "", e.key); rec.Code != 200 {
		t.Fatalf("models: %d", rec.Code)
	}
	logs, err := e.st.ListRequestLogs(context.Background(), "", time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 0 {
		t.Fatalf("GET /v1/models logged: %+v", logs)
	}
	e.do("POST", "/v1/images/generations", `{"model":"nope","prompt":"x"}`, e.key)
	logs, err = e.st.ListRequestLogs(context.Background(), "", time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].Endpoint != "POST /v1/images/generations" {
		t.Fatalf("image call not logged: %+v", logs)
	}
}
