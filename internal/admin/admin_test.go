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
	"ai-media-gateway/internal/netproxy"
	"ai-media-gateway/internal/openai"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/provider/fal"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/server"
	"ai-media-gateway/internal/store"

	_ "ai-media-gateway/internal/provider/elevenlabs"
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
	pool := accounts.New(st, box, config.Cooldowns{}, netproxy.NewClients(0), nil)
	e.srv = server.New(server.Deps{
		Config: cfg, Store: st, Pool: pool,
		Admin: New(st, box, netproxy.NewClients(0)), OpenAI: openai.New(st, pool),
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
	_, plain, err := e.st.CreateAPIKey(context.Background(), "canvas", nil)
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

func TestModelRename(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/admin/providers", `{"id":"p1","type":"fake"}`)
	if rec := e.do("POST", "/admin/models", `{"id":"m-old","provider_id":"p1","kind":"image"}`); rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	e.do("POST", "/admin/models", `{"id":"m-taken","provider_id":"p1","kind":"image"}`)

	rec := e.do("PATCH", "/admin/models/m-old", `{"id":"m-new"}`)
	if rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	var m struct {
		ID string `json:"id"`
	}
	e.json(rec, &m)
	if m.ID != "m-new" {
		t.Fatalf("response id: %+v", m)
	}
	if rec := e.do("GET", "/admin/models/m-new", ""); rec.Code != 200 {
		t.Fatalf("get new id: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/models/m-old", ""); rec.Code != 404 {
		t.Fatalf("get old id: %d", rec.Code)
	}
	if rec := e.do("PATCH", "/admin/models/m-new", `{"id":"m-taken"}`); rec.Code != 409 {
		t.Fatalf("rename onto existing: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PATCH", "/admin/models/m-new", `{"id":"Bad!"}`); rec.Code != 400 {
		t.Fatalf("invalid id: %d", rec.Code)
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

	// secret endpoint returns the same plaintext that create returned
	rec = e.do("GET", "/admin/keys/"+k.ID+"/secret", "")
	if rec.Code != 200 {
		t.Fatalf("secret: %d %s", rec.Code, rec.Body)
	}
	var sec struct {
		Key string `json:"key"`
	}
	e.json(rec, &sec)
	if sec.Key != k.Key {
		t.Fatalf("secret key %q != created key", sec.Key)
	}
	// a key created without key_enc (pre-migration) can't be recovered
	legacy, _, err := e.st.CreateAPIKey(context.Background(), "legacy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", "/admin/keys/"+legacy.ID+"/secret", ""); rec.Code != 400 {
		t.Fatalf("legacy secret: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/keys/key_ghost/secret", ""); rec.Code != 404 {
		t.Fatalf("unknown secret: %d", rec.Code)
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
	found := false
	for _, typ := range out.Data {
		if typ == "fake" {
			found = true
		}
	}
	if !found {
		t.Fatalf("types: %v", out.Data)
	}
}

func TestUpstreamModels(t *testing.T) {
	e := newEnv(t)

	// unknown provider → 404
	if rec := e.do("GET", "/admin/providers/ghost/upstream-models", ""); rec.Code != 404 {
		t.Fatalf("unknown provider: %d %s", rec.Code, rec.Body)
	}
	// fake adapter is not a ModelLister → 400
	e.do("POST", "/admin/providers", `{"id":"p1","type":"fake"}`)
	if rec := e.do("GET", "/admin/providers/p1/upstream-models", ""); rec.Code != 400 {
		t.Fatalf("non-lister: %d %s", rec.Code, rec.Body)
	}
	// elevenlabs account without a key → 400 Chinese message
	e.do("POST", "/admin/providers", `{"id":"el","type":"elevenlabs"}`)
	e.do("POST", "/admin/providers/el/accounts", `{"name":"nokey"}`)
	rec := e.do("GET", "/admin/providers/el/upstream-models", "")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "该平台还没有可用的 key") {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body)
	}

	// fal happy path against a fake catalog (anonymous — no account needed)
	cat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{
				"endpoint_id": "fal-ai/flux/dev",
				"metadata":    map[string]any{"display_name": "FLUX.1 [dev]", "category": "text-to-image", "status": "active"},
			}},
			"next_cursor": "c2",
		})
	}))
	defer cat.Close()
	defer fal.SetCatalogBase(cat.URL)()

	e.do("POST", "/admin/providers", `{"id":"fal1","type":"fal"}`)
	rec = e.do("GET", "/admin/providers/fal1/upstream-models?q=flux&category=text-to-image", "")
	if rec.Code != 200 {
		t.Fatalf("fal list: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Data []struct {
			ID       string `json:"id"`
			Kind     string `json:"kind"`
			Category string `json:"category"`
		} `json:"data"`
		NextCursor string `json:"next_cursor"`
	}
	e.json(rec, &out)
	if len(out.Data) != 1 || out.Data[0].ID != "fal-ai/flux/dev" ||
		out.Data[0].Kind != "image" || out.Data[0].Category != "text-to-image" || out.NextCursor != "c2" {
		t.Fatalf("data: %+v", out)
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

func TestSettingsAndProxyURL(t *testing.T) {
	e := newEnv(t)

	// settings round-trip
	rec := e.do("GET", "/admin/settings", "")
	if rec.Code != 200 {
		t.Fatalf("get settings: %d", rec.Code)
	}
	var s map[string]string
	e.json(rec, &s)
	if s["proxy_url"] != "" {
		t.Fatalf("initial proxy_url: %q", s["proxy_url"])
	}
	rec = e.do("PATCH", "/admin/settings", `{"proxy_url":"socks5://u:p@127.0.0.1:1080"}`)
	e.json(rec, &s)
	if rec.Code != 200 || s["proxy_url"] != "socks5://u:p@127.0.0.1:1080" {
		t.Fatalf("patch settings: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"proxy_url":"ftp://x"}`, `{"proxy_url":"http://"}`, `{"proxy_url":"::::"}`} {
		if rec := e.do("PATCH", "/admin/settings", bad); rec.Code != 400 {
			t.Fatalf("bad settings %s: %d", bad, rec.Code)
		}
	}
	rec = e.do("PATCH", "/admin/settings", `{"proxy_url":""}`)
	e.json(rec, &s)
	if rec.Code != 200 || s["proxy_url"] != "" {
		t.Fatalf("clear settings: %d %s", rec.Code, rec.Body)
	}

	// provider proxy_url: create + patch + validation
	rec = e.do("POST", "/admin/providers", `{"id":"p1","type":"fake","proxy_url":"http://127.0.0.1:7890"}`)
	if rec.Code != 201 {
		t.Fatalf("create provider: %d %s", rec.Code, rec.Body)
	}
	var p struct {
		ProxyURL string `json:"proxy_url"`
	}
	e.json(rec, &p)
	if p.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("provider proxy_url: %q", p.ProxyURL)
	}
	rec = e.do("PATCH", "/admin/providers/p1", `{"proxy_url":"direct"}`)
	e.json(rec, &p)
	if rec.Code != 200 || p.ProxyURL != "direct" {
		t.Fatalf("patch provider: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PATCH", "/admin/providers/p1", `{"proxy_url":"gopher://x"}`); rec.Code != 400 {
		t.Fatalf("bad provider proxy: %d", rec.Code)
	}
	if rec := e.do("POST", "/admin/providers", `{"id":"p2","type":"fake","proxy_url":"nonsense"}`); rec.Code != 400 {
		t.Fatalf("bad create proxy: %d", rec.Code)
	}

	// account proxy_url: create + patch + validation
	rec = e.do("POST", "/admin/providers/p1/accounts", `{"api_key":"sk-x","proxy_url":"socks5h://h:1080"}`)
	if rec.Code != 201 {
		t.Fatalf("create account: %d %s", rec.Code, rec.Body)
	}
	var acc struct {
		ID       string `json:"id"`
		ProxyURL string `json:"proxy_url"`
	}
	e.json(rec, &acc)
	if acc.ProxyURL != "socks5h://h:1080" {
		t.Fatalf("account proxy_url: %q", acc.ProxyURL)
	}
	rec = e.do("PATCH", "/admin/accounts/"+acc.ID, `{"proxy_url":""}`)
	e.json(rec, &acc)
	if rec.Code != 200 || acc.ProxyURL != "" {
		t.Fatalf("patch account: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PATCH", "/admin/accounts/"+acc.ID, `{"proxy_url":"ftp://x"}`); rec.Code != 400 {
		t.Fatalf("bad account proxy: %d", rec.Code)
	}
}

func TestProxyTestEndpoint(t *testing.T) {
	e := newEnv(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(418)
	}))
	defer target.Close()

	rec := e.do("POST", "/admin/proxy/test", `{"proxy_url":"direct","target":"`+target.URL+`"}`)
	if rec.Code != 200 {
		t.Fatalf("proxy test: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		OK        bool   `json:"ok"`
		Status    int    `json:"status"`
		LatencyMS int64  `json:"latency_ms"`
		Error     string `json:"error"`
	}
	e.json(rec, &out)
	if !out.OK || out.Status != 418 {
		t.Fatalf("direct test: %+v", out)
	}

	// unreachable proxy → ok:false, masked password
	rec = e.do("POST", "/admin/proxy/test", `{"proxy_url":"http://u:sekret@127.0.0.1:1","target":"`+target.URL+`"}`)
	e.json(rec, &out)
	if out.OK || out.Error == "" {
		t.Fatalf("bad proxy test: %+v", out)
	}
	if strings.Contains(out.Error, "sekret") {
		t.Fatalf("password leaked: %s", out.Error)
	}

	// invalid proxy value → 400
	if rec := e.do("POST", "/admin/proxy/test", `{"proxy_url":"ftp://x"}`); rec.Code != 400 {
		t.Fatalf("invalid proxy: %d", rec.Code)
	}
}

func TestProxyPool(t *testing.T) {
	e := newEnv(t)

	// validation: name required, url must be a real proxy URL
	for _, bad := range []string{
		`{"name":"","url":"http://h:1"}`,
		`{"name":"x","url":""}`,
		`{"name":"x","url":"direct"}`,
		`{"name":"x","url":"proxy:px_1"}`,
		`{"name":"x","url":"ftp://h"}`,
	} {
		if rec := e.do("POST", "/admin/proxies", bad); rec.Code != 400 {
			t.Fatalf("bad proxy %s: %d %s", bad, rec.Code, rec.Body)
		}
	}

	rec := e.do("POST", "/admin/proxies", `{"name":"hk","url":"http://u:pw@127.0.0.1:7890"}`)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var px struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		URLMasked string   `json:"url_masked"`
		UsedBy    []string `json:"used_by"`
	}
	e.json(rec, &px)
	if !strings.HasPrefix(px.ID, "px_") || px.URLMasked != "http://u:***@127.0.0.1:7890" || len(px.UsedBy) != 0 {
		t.Fatalf("created: %+v", px)
	}
	if strings.Contains(rec.Body.String(), "pw") && !strings.Contains(rec.Body.String(), `"url"`) {
		t.Fatal("url leaked but raw url field is expected")
	}

	// list
	rec = e.do("GET", "/admin/proxies", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"hk"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}

	// patch + validation
	rec = e.do("PATCH", "/admin/proxies/"+px.ID, `{"name":"hk-2","url":"socks5://h:1080"}`)
	if rec.Code != 200 {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	e.json(rec, &px)
	if px.Name != "hk-2" || px.URLMasked != "socks5://h:1080" {
		t.Fatalf("patched: %+v", px)
	}
	if rec := e.do("PATCH", "/admin/proxies/"+px.ID, `{"url":"nope"}`); rec.Code != 400 {
		t.Fatalf("bad patch url: %d", rec.Code)
	}
	if rec := e.do("PATCH", "/admin/proxies/px_ghost", `{"name":"x"}`); rec.Code != 404 {
		t.Fatalf("patch ghost: %d", rec.Code)
	}

	// reference validation on provider proxy_url
	e.do("POST", "/admin/providers", `{"id":"p1","type":"fake"}`)
	rec = e.do("PATCH", "/admin/providers/p1", `{"proxy_url":"proxy:`+px.ID+`"}`)
	if rec.Code != 200 {
		t.Fatalf("provider ref: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PATCH", "/admin/providers/p1", `{"proxy_url":"proxy:px_ghost"}`); rec.Code != 400 {
		t.Fatalf("ghost ref: %d", rec.Code)
	}
	// account + global refs too
	rec = e.do("POST", "/admin/providers/p1/accounts", `{"api_key":"sk-x","proxy_url":"proxy:`+px.ID+`"}`)
	if rec.Code != 201 {
		t.Fatalf("account ref: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("PATCH", "/admin/settings", `{"proxy_url":"proxy:`+px.ID+`"}`)
	if rec.Code != 200 {
		t.Fatalf("settings ref: %d %s", rec.Code, rec.Body)
	}

	// used_by populated → delete 409
	rec = e.do("GET", "/admin/proxies", "")
	var list struct {
		Proxies []struct {
			ID     string   `json:"id"`
			UsedBy []string `json:"used_by"`
		} `json:"proxies"`
	}
	e.json(rec, &list)
	if len(list.Proxies) != 1 || len(list.Proxies[0].UsedBy) != 3 {
		t.Fatalf("used_by: %+v", list.Proxies)
	}
	rec = e.do("DELETE", "/admin/proxies/"+px.ID, "")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "正在被使用") {
		t.Fatalf("delete in use: %d %s", rec.Code, rec.Body)
	}

	// clear refs → delete 204
	e.do("PATCH", "/admin/settings", `{"proxy_url":""}`)
	e.do("PATCH", "/admin/providers/p1", `{"proxy_url":""}`)
	rec = e.do("GET", "/admin/providers/p1/accounts", "")
	var accs struct {
		Accounts []struct {
			ID string `json:"id"`
		} `json:"accounts"`
	}
	e.json(rec, &accs)
	e.do("PATCH", "/admin/accounts/"+accs.Accounts[0].ID, `{"proxy_url":""}`)
	if rec := e.do("DELETE", "/admin/proxies/"+px.ID, ""); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
}
