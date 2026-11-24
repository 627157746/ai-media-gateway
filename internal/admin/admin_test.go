package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/config"
	"ai-media-gateway/internal/openai"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/server"
	"ai-media-gateway/internal/store"
)

type fakeAdapter struct{}

func (fakeAdapter) Type() string { return "fake" }

func init() { provider.Register("fake", fakeAdapter{}) }

type env struct {
	t   *testing.T
	st  *store.Store
	srv http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{AdminToken: "adm", MaxBodyBytes: 1 << 20, CORSOrigins: []string{"*"}}
	e := &env{t: t, st: st}
	box := secret.New("mk")
	pool := accounts.New(st, box, config.Cooldowns{}, http.DefaultClient, nil)
	e.srv = server.New(server.Deps{
		Config: cfg, Store: st, Pool: pool,
		Admin: New(st, box), OpenAI: openai.New(st, pool),
	})
	return e
}

func (e *env) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer adm")
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func (e *env) json(rec *httptest.ResponseRecorder, v any) {
	e.t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		e.t.Fatalf("body=%s: %v", rec.Body, err)
	}
}

func TestProviderAccountModelFlow(t *testing.T) {
	e := newEnv(t)

	// unknown type rejected
	rec := e.do("POST", "/admin/providers", `{"id":"bad","type":"nope"}`)
	if rec.Code != 400 {
		t.Fatalf("unknown type: %d %s", rec.Code, rec.Body)
	}
	// missing id rejected
	if rec := e.do("POST", "/admin/providers", `{"type":"fake"}`); rec.Code != 400 {
		t.Fatalf("missing id: %d", rec.Code)
	}
	// invalid id charset
	if rec := e.do("POST", "/admin/providers", `{"id":"BAD","type":"fake"}`); rec.Code != 400 {
		t.Fatalf("bad id: %d", rec.Code)
	}

	rec = e.do("POST", "/admin/providers", `{"id":"p1","type":"fake","base_url":"https://api.x"}`)
	if rec.Code != 201 {
		t.Fatalf("create provider: %d %s", rec.Code, rec.Body)
	}

	// account with api key; masked in response
	rec = e.do("POST", "/admin/providers/p1/accounts", `{"name":"main","api_key":"sk-abc1234567","weight":2}`)
	if rec.Code != 201 {
		t.Fatalf("create account: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "sk-abc1234567") {
		t.Fatal("plaintext key leaked in create response")
	}
	var acc struct {
		ID           string `json:"id"`
		APIKeyMasked string `json:"api_key_masked"`
		State        string `json:"state"`
	}
	e.json(rec, &acc)
	if !strings.HasPrefix(acc.ID, "acc_") || acc.APIKeyMasked != "sk-a…4567" || acc.State != "active" {
		t.Fatalf("account out: %+v", acc)
	}

	// GET must not leak the key either
	rec = e.do("GET", "/admin/providers/p1/accounts", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "sk-abc1234567") || !strings.Contains(rec.Body.String(), "api_key_masked") {
		t.Fatalf("list accounts: %d %s", rec.Code, rec.Body)
	}

	// model
	rec = e.do("POST", "/admin/models", `{"id":"img-1","provider_id":"p1","kind":"image","upstream":"fal/flux"}`)
	if rec.Code != 201 {
		t.Fatalf("create model: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/admin/models", `{"id":"bad-1","provider_id":"p1","kind":"text"}`); rec.Code != 400 {
		t.Fatalf("bad kind: %d", rec.Code)
	}
	if rec := e.do("POST", "/admin/models", `{"id":"x-1","provider_id":"ghost","kind":"image"}`); rec.Code != 400 {
		t.Fatalf("bad provider: %d", rec.Code)
	}
	if rec := e.do("POST", "/admin/models", `{"id":"seedance-2.5","provider_id":"p1","kind":"video"}`); rec.Code != 201 {
		t.Fatalf("dotted model id: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("DELETE", "/admin/models/seedance-2.5", ""); rec.Code != 204 {
		t.Fatalf("delete dotted model: %d", rec.Code)
	}
	for _, id := range []string{".", "..", ".x", "a/b", "A-1"} {
		if rec := e.do("POST", "/admin/models", `{"id":"`+id+`","provider_id":"p1","kind":"image"}`); rec.Code != 400 {
			t.Fatalf("model id %q: %d", id, rec.Code)
		}
	}

	// /v1/models requires gateway key — create one and check listing
	_, plain, err := e.st.CreateAPIKey(context.Background(), "canvas")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec2 := httptest.NewRecorder()
	e.srv.ServeHTTP(rec2, req)
	if !strings.Contains(rec2.Body.String(), `"id":"img-1"`) {
		t.Fatalf("v1 models: %s", rec2.Body)
	}

	// reset endpoint exists and clears state
	rec = e.do("POST", "/admin/accounts/"+acc.ID+"/reset", "")
	if rec.Code != 200 {
		t.Fatalf("reset: %d", rec.Code)
	}

	// delete provider with children → 409
	if rec := e.do("DELETE", "/admin/providers/p1", ""); rec.Code != 409 {
		t.Fatalf("delete with children: %d", rec.Code)
	}
	// delete account → then model → then provider
	if rec := e.do("DELETE", "/admin/accounts/"+acc.ID, ""); rec.Code != 204 {
		t.Fatalf("delete account: %d", rec.Code)
	}
	if rec := e.do("DELETE", "/admin/models/img-1", ""); rec.Code != 204 {
		t.Fatalf("delete model: %d", rec.Code)
	}
	if rec := e.do("DELETE", "/admin/providers/p1", ""); rec.Code != 204 {
		t.Fatalf("delete provider: %d", rec.Code)
	}
}

func TestPatchPartial(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/admin/providers", `{"id":"p1","type":"fake"}`)
	e.do("POST", "/admin/providers/p1/accounts", `{"name":"a","api_key":"k"}`)
	var out struct {
		Accounts []struct {
			ID string `json:"id"`
		} `json:"accounts"`
	}
	e.json(e.do("GET", "/admin/providers/p1/accounts", ""), &out)
	accID := out.Accounts[0].ID

	rec := e.do("PATCH", "/admin/accounts/"+accID, `{"weight":5,"enabled":false}`)
	if rec.Code != 200 {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	var a struct {
		Weight  int    `json:"weight"`
		Enabled bool   `json:"enabled"`
		State   string `json:"state"`
		Name    string `json:"name"`
	}
	e.json(rec, &a)
	if a.Weight != 5 || a.Enabled || a.State != "disabled" || a.Name != "a" {
		t.Fatalf("patched: %+v", a)
	}
}

func TestKeysAPI(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", "/admin/keys", `{"name":"k1"}`)
	if rec.Code != 201 {
		t.Fatalf("create key: %d", rec.Code)
	}
	var k struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	e.json(rec, &k)
	if !strings.HasPrefix(k.Key, "sk-mg-") {
		t.Fatalf("key: %+v", k)
	}
	rec = e.do("GET", "/admin/keys", "")
	if strings.Contains(rec.Body.String(), k.Key) || strings.Contains(rec.Body.String(), "key_hash") {
		t.Fatalf("key material in list: %s", rec.Body)
	}
	var list struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	e.json(rec, &list)
	if len(list.Keys) != 1 || list.Keys[0].ID != k.ID {
		t.Fatalf("list: %v", list)
	}
	if rec := e.do("DELETE", "/admin/keys/"+k.ID, ""); rec.Code != 204 {
		t.Fatalf("delete key: %d", rec.Code)
	}
	// deleted key no longer authenticates
	if _, err := e.st.AuthenticateKey(context.Background(), k.Key); err == nil {
		t.Fatal("deleted key still authenticates")
	}
}

func TestStatusAndLogs(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/admin/providers", `{"id":"p1","type":"fake"}`)
	e.do("POST", "/admin/providers/p1/accounts", `{"name":"a"}`)
	rec := e.do("GET", "/admin/status", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"state":"active"`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/admin/tasks", ""); rec.Code != 200 {
		t.Fatalf("tasks: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/logs", ""); rec.Code != 200 {
		t.Fatalf("logs: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/logs?since=notadate", ""); rec.Code != 400 {
		t.Fatalf("bad since: %d", rec.Code)
	}
}

func TestProviderTypes(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/admin/provider-types", "")
	if rec.Code != 200 {
		t.Fatalf("provider-types: %d", rec.Code)
	}
	var out struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) == 0 || out.Data[0] != "fake" {
		t.Fatalf("types: %v", out.Data)
	}
}

// weight must be >= 1 on create and patch; omitted weight defaults to 1.
func TestAccountWeightValidation(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("POST", "/admin/providers", `{"id":"p1","type":"fake"}`); rec.Code != 201 {
		t.Fatalf("create provider: %d %s", rec.Code, rec.Body)
	}
	rec := e.do("POST", "/admin/providers/p1/accounts",
		`{"id":"a0","api_key":"k","weight":0}`)
	if rec.Code != 400 {
		t.Fatalf("create weight=0: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("POST", "/admin/providers/p1/accounts", `{"id":"a1","api_key":"k"}`)
	var a struct {
		Weight int `json:"weight"`
	}
	e.json(rec, &a)
	if rec.Code != 201 || a.Weight != 1 {
		t.Fatalf("create default: %d %+v", rec.Code, a)
	}
	rec = e.do("PATCH", "/admin/accounts/a1", `{"weight":0}`)
	if rec.Code != 400 {
		t.Fatalf("patch weight=0: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do("PATCH", "/admin/accounts/a1", `{"weight":3}`)
	if rec.Code != 200 {
		t.Fatalf("patch weight=3: %d %s", rec.Code, rec.Body.String())
	}
}
