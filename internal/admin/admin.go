package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/netproxy"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/store"
)

var idRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// modelIDRE also allows dots (e.g. seedance-2.5) but not a leading one, so
// "." / ".." can never become a path segment under /admin/models/.
var modelIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

type handler struct {
	st      *store.Store
	box     *secret.Box
	clients *netproxy.Clients
	now     func() time.Time
	dbPath  string // reference-audio samples live next to the SQLite file
}

// New returns the admin API handler (already mounted under /admin/).
// dbPath locates the reference-audio directory; empty disables cloning.
func New(st *store.Store, box *secret.Box, clients *netproxy.Clients, dbPath string) http.Handler {
	if clients == nil {
		clients = netproxy.NewClients(0)
	}
	h := &handler{st: st, box: box, clients: clients, now: time.Now, dbPath: dbPath}
	m := http.NewServeMux()
	m.HandleFunc("GET /admin/providers", h.listProviders)
	m.HandleFunc("POST /admin/providers", h.createProvider)
	m.HandleFunc("GET /admin/providers/{id}", h.getProvider)
	m.HandleFunc("PATCH /admin/providers/{id}", h.patchProvider)
	m.HandleFunc("DELETE /admin/providers/{id}", h.deleteProvider)
	m.HandleFunc("GET /admin/providers/{id}/upstream-models", h.upstreamModels)
	m.HandleFunc("GET /admin/providers/{id}/endpoint-schema", h.endpointSchema)
	m.HandleFunc("GET /admin/providers/{id}/accounts", h.listAccounts)
	m.HandleFunc("POST /admin/providers/{id}/accounts", h.createAccount)
	m.HandleFunc("GET /admin/accounts/{id}", h.getAccount)
	m.HandleFunc("PATCH /admin/accounts/{id}", h.patchAccount)
	m.HandleFunc("DELETE /admin/accounts/{id}", h.deleteAccount)
	m.HandleFunc("POST /admin/accounts/{id}/reset", h.resetAccount)
	m.HandleFunc("GET /admin/models", h.listModels)
	m.HandleFunc("POST /admin/models", h.createModel)
	m.HandleFunc("GET /admin/models/{id}", h.getModel)
	m.HandleFunc("PATCH /admin/models/{id}", h.patchModel)
	m.HandleFunc("DELETE /admin/models/{id}", h.deleteModel)
	m.HandleFunc("GET /admin/keys", h.listKeys)
	m.HandleFunc("POST /admin/keys", h.createKey)
	m.HandleFunc("GET /admin/keys/{id}/secret", h.keySecret)
	m.HandleFunc("DELETE /admin/keys/{id}", h.deleteKey)
	m.HandleFunc("GET /admin/tasks", h.listTasks)
	m.HandleFunc("GET /admin/logs", h.listLogs)
	m.HandleFunc("GET /admin/status", h.status)
	m.HandleFunc("GET /admin/provider-types", h.providerTypes)
	m.HandleFunc("GET /admin/settings", h.getSettings)
	m.HandleFunc("PATCH /admin/settings", h.patchSettings)
	m.HandleFunc("POST /admin/proxy/test", h.proxyTest)
	m.HandleFunc("GET /admin/proxies", h.listProxies)
	m.HandleFunc("POST /admin/proxies", h.createProxy)
	m.HandleFunc("PATCH /admin/proxies/{id}", h.patchProxy)
	m.HandleFunc("DELETE /admin/proxies/{id}", h.deleteProxy)
	m.HandleFunc("GET /admin/voices", h.listVoices)
	m.HandleFunc("POST /admin/voices", h.createVoice)
	m.HandleFunc("PATCH /admin/voices/{id}", h.patchVoice)
	m.HandleFunc("DELETE /admin/voices/{id}", h.deleteVoice)
	m.HandleFunc("GET /admin/providers/{id}/upstream-voices", h.upstreamVoices)
	m.HandleFunc("POST /admin/voices/clone", h.cloneVoice)
	return m
}

func (h *handler) providerTypes(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"data": provider.Types()})
}

func fail(w http.ResponseWriter, err error) { httpx.WriteError(w, err) }

func bad(format string, args ...any) error { return httpx.BadRequest(format, args...) }

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, bad("invalid JSON body: %v", err))
		return false
	}
	return true
}

func validID(id string) bool { return idRE.MatchString(id) }

func validOptions(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var o map[string]any
	return json.Unmarshal(raw, &o) == nil
}

// validProxyURL accepts "", "direct", or a URL with scheme http/https/
// socks5/socks5h and a non-empty host.
func validProxyURL(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "direct" {
		return true
	}
	return validProxyScheme(v)
}

// validProxyScheme accepts only a real proxy URL — not "", "direct" or a
// proxy:<id> pool reference (those are meaningful only as a proxy_url value).
func validProxyScheme(v string) bool {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return true
	}
	return false
}

// validProxyValue accepts anything validProxyURL accepts plus a "proxy:<id>"
// reference to an existing pool entry.
func (h *handler) validProxyValue(ctx context.Context, v string) bool {
	v = strings.TrimSpace(v)
	if validProxyURL(v) {
		return true
	}
	if id, ok := strings.CutPrefix(v, "proxy:"); ok && id != "" {
		_, err := h.st.GetProxy(ctx, id)
		return err == nil
	}
	return false
}

// ---- providers ----

func (h *handler) listProviders(w http.ResponseWriter, r *http.Request) {
	ps, err := h.st.ListProviders(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"providers": ps})
}

func (h *handler) getProvider(w http.ResponseWriter, r *http.Request) {
	p, err := h.st.GetProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

type providerIn struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	BaseURL  string          `json:"base_url"`
	ProxyURL string          `json:"proxy_url"`
	Options  json.RawMessage `json:"options"`
	Enabled  *bool           `json:"enabled"`
}

func (h *handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var in providerIn
	if !readJSON(w, r, &in) {
		return
	}
	if !validID(in.ID) {
		fail(w, bad("provider id required and must match [a-z0-9-]+"))
		return
	}
	if in.Type == "" {
		fail(w, bad("type required"))
		return
	}
	if _, ok := provider.Get(in.Type); !ok {
		fail(w, bad("unknown provider type %q (registered: %s)", in.Type, strings.Join(provider.Types(), ", ")))
		return
	}
	if len(in.Options) > 0 && !validOptions(in.Options) {
		fail(w, bad("options must be a JSON object"))
		return
	}
	if !h.validProxyValue(r.Context(), in.ProxyURL) {
		fail(w, bad("proxy_url 无效：留空表示继承，direct 表示直连，或填 http/https/socks5/socks5h 代理地址"))
		return
	}
	p := &store.Provider{
		ID:       in.ID,
		Type:     in.Type,
		BaseURL:  in.BaseURL,
		ProxyURL: strings.TrimSpace(in.ProxyURL),
		Options:  in.Options,
		Enabled:  in.Enabled == nil || *in.Enabled,
	}
	if err := h.st.CreateProvider(r.Context(), p); err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, p)
}

type providerPatchIn struct {
	Type     *string          `json:"type"`
	BaseURL  *string          `json:"base_url"`
	ProxyURL *string          `json:"proxy_url"`
	Options  *json.RawMessage `json:"options"`
	Enabled  *bool            `json:"enabled"`
}

func (h *handler) patchProvider(w http.ResponseWriter, r *http.Request) {
	var in providerPatchIn
	if !readJSON(w, r, &in) {
		return
	}
	if in.Type != nil {
		if _, ok := provider.Get(*in.Type); !ok {
			fail(w, bad("unknown provider type %q", *in.Type))
			return
		}
	}
	if in.Options != nil && !validOptions(*in.Options) {
		fail(w, bad("options must be a JSON object"))
		return
	}
	if in.ProxyURL != nil {
		v := strings.TrimSpace(*in.ProxyURL)
		if !h.validProxyValue(r.Context(), v) {
			fail(w, bad("proxy_url 无效：留空表示继承，direct 表示直连，或填 http/https/socks5/socks5h 代理地址"))
			return
		}
		in.ProxyURL = &v
	}
	if err := h.st.UpdateProvider(r.Context(), r.PathValue("id"), store.ProviderPatch{
		Type:     in.Type,
		BaseURL:  in.BaseURL,
		ProxyURL: in.ProxyURL,
		Options:  in.Options,
		Enabled:  in.Enabled,
	}); err != nil {
		fail(w, err)
		return
	}
	h.getProvider(w, r)
}

// upstreamCall picks the highest-priority enabled account with a key
// (non-cooling preferred), resolves the effective proxy, decrypts the key and
// builds a provider.Call for an admin-side upstream query. The returned key is
// the plaintext one — callers must pass errors through scrubKey.
//
// allowAnonymous is only for fal, whose catalog endpoint needs no key.
func (h *handler) upstreamCall(ctx context.Context, p *store.Provider, allowAnonymous bool) (provider.Call, string, error) {
	accs, err := h.st.ListAccounts(ctx, p.ID)
	if err != nil {
		return provider.Call{}, "", err
	}
	var pick, cooling *store.Account
	now := h.now()
	for _, a := range accs { // ListAccounts is ordered by priority DESC
		if !a.Enabled || len(a.APIKeyEnc) == 0 {
			continue
		}
		if a.CooldownUntil != nil && a.CooldownUntil.After(now) {
			if cooling == nil {
				cooling = a
			}
			continue
		}
		pick = a
		break
	}
	if pick == nil {
		pick = cooling
	}
	if pick == nil && !allowAnonymous {
		return provider.Call{}, "", bad("该平台还没有可用的 key，请先添加")
	}
	global, err := h.st.GetSetting(ctx, "proxy_url")
	if err != nil {
		return provider.Call{}, "", err
	}
	acctProxy := ""
	if pick != nil {
		acctProxy = pick.ProxyURL
	}
	proxyURL, err := accounts.ResolveProxy(ctx, h.st, netproxy.Effective(acctProxy, p.ProxyURL, global))
	if err != nil {
		return provider.Call{}, "", err
	}
	call := provider.Call{
		Provider: provider.ProviderConfig{ID: p.ID, Type: p.Type, BaseURL: p.BaseURL, ProxyURL: p.ProxyURL, Options: p.Options},
	}
	call.HTTP, call.Stream = h.clients.For(proxyURL)
	key := ""
	if pick != nil {
		call.Account = provider.AccountConfig{
			ID: pick.ID, Name: pick.Name, BaseURL: pick.BaseURL, Options: pick.Options,
		}
		raw, err := h.box.Open(pick.APIKeyEnc)
		if err != nil {
			return provider.Call{}, "", fmt.Errorf("decrypt key of account %s: %w", pick.ID, err)
		}
		key = string(raw)
		call.Account.APIKey = key
	}
	return call, key, nil
}

// upstreamModels lists a provider's model catalog via the adapter's
// ModelLister, using the highest-priority enabled account that has a key
// (non-cooling preferred). fal may query anonymously; other types 400
// without a key. The key never reaches the response.
func (h *handler) upstreamModels(w http.ResponseWriter, r *http.Request) {
	p, err := h.st.GetProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	adapter, ok := provider.Get(p.Type)
	if !ok {
		fail(w, bad("unknown provider type %q", p.Type))
		return
	}
	lister, ok := adapter.(provider.ModelLister)
	if !ok {
		fail(w, bad("平台 %s 不支持获取上游模型列表", p.Type))
		return
	}
	call, key, err := h.upstreamCall(r.Context(), p, p.Type == "fal")
	if err != nil {
		fail(w, err)
		return
	}
	qs := r.URL.Query()
	page, err := lister.ListModels(r.Context(), call, provider.ModelQuery{
		Q:        qs.Get("q"),
		Category: qs.Get("category"),
		Cursor:   qs.Get("cursor"),
		IDs:      qs["endpoint_id"],
	})
	if err != nil {
		fail(w, scrubKey(err, key))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"data":        page.Models,
		"next_cursor": page.NextCursor,
	})
}

// endpointSchema reports one upstream endpoint's declared input fields, so the
// admin UI can show what a model accepts before it is created. The gateway
// derives a model's capabilities from this rather than from a hardcoded table
// that goes stale whenever the vendor adds or changes a model.
func (h *handler) endpointSchema(w http.ResponseWriter, r *http.Request) {
	p, err := h.st.GetProvider(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	adapter, ok := provider.Get(p.Type)
	if !ok {
		fail(w, bad("unknown provider type %q", p.Type))
		return
	}
	lister, ok := adapter.(provider.SchemaLister)
	if !ok {
		fail(w, bad("平台 %s 不提供端点参数说明", p.Type))
		return
	}
	endpoint := r.URL.Query().Get("endpoint_id")
	if endpoint == "" {
		fail(w, bad("endpoint_id is required"))
		return
	}
	call, key, err := h.upstreamCall(r.Context(), p, p.Type == "fal")
	if err != nil {
		fail(w, err)
		return
	}
	schema, err := lister.EndpointSchema(r.Context(), call, endpoint)
	if err != nil {
		fail(w, scrubKey(err, key))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, schema)
}

// scrubKey removes the decrypted API key from an upstream error (a vendor
// could echo it back in an error message).
func scrubKey(err error, key string) error {
	if err == nil || key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	var ue *media.UpstreamError
	if errors.As(err, &ue) {
		cp := *ue
		cp.Message = strings.ReplaceAll(cp.Message, key, "***")
		cp.Code = strings.ReplaceAll(cp.Code, key, "***")
		return &cp
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "***"))
}

func (h *handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := h.st.DeleteProvider(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- accounts ----

func (h *handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	accs, err := h.st.ListAccounts(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]accountOut, 0, len(accs))
	for _, a := range accs {
		out = append(out, h.accountOut(a))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (h *handler) getAccount(w http.ResponseWriter, r *http.Request) {
	a, err := h.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.accountOut(a))
}

type accountIn struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	APIKey   string          `json:"api_key"`
	BaseURL  string          `json:"base_url"`
	ProxyURL string          `json:"proxy_url"`
	Options  json.RawMessage `json:"options"`
	Priority *int            `json:"priority"`
	Weight   *int            `json:"weight"`
	Enabled  *bool           `json:"enabled"`
}

func (h *handler) createAccount(w http.ResponseWriter, r *http.Request) {
	provID := r.PathValue("id")
	if _, err := h.st.GetProvider(r.Context(), provID); err != nil {
		fail(w, err)
		return
	}
	var in accountIn
	if !readJSON(w, r, &in) {
		return
	}
	if in.ID == "" {
		in.ID = "acc_" + store.RandHex(6)
	} else if !validID(in.ID) {
		fail(w, bad("account id must match [a-z0-9-]+"))
		return
	}
	if len(in.Options) > 0 && !validOptions(in.Options) {
		fail(w, bad("options must be a JSON object"))
		return
	}
	if !h.validProxyValue(r.Context(), in.ProxyURL) {
		fail(w, bad("proxy_url 无效：留空表示继承，direct 表示直连，或填 http/https/socks5/socks5h 代理地址"))
		return
	}
	var enc []byte
	if in.APIKey != "" {
		var err error
		enc, err = h.box.Seal([]byte(in.APIKey))
		if err != nil {
			fail(w, err)
			return
		}
	}
	a := &store.Account{
		ID:         in.ID,
		ProviderID: provID,
		Name:       in.Name,
		APIKeyEnc:  enc,
		BaseURL:    in.BaseURL,
		ProxyURL:   strings.TrimSpace(in.ProxyURL),
		Options:    in.Options,
		Enabled:    in.Enabled == nil || *in.Enabled,
		Weight:     1,
	}
	if in.Priority != nil {
		a.Priority = *in.Priority
	}
	if in.Weight != nil {
		if *in.Weight < 1 {
			fail(w, bad("weight must be >= 1"))
			return
		}
		a.Weight = *in.Weight
	}
	if err := h.st.CreateAccount(r.Context(), a); err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, h.accountOut(a))
}

type accountPatchIn struct {
	Name     *string          `json:"name"`
	APIKey   *string          `json:"api_key"`
	BaseURL  *string          `json:"base_url"`
	ProxyURL *string          `json:"proxy_url"`
	Options  *json.RawMessage `json:"options"`
	Priority *int             `json:"priority"`
	Weight   *int             `json:"weight"`
	Enabled  *bool            `json:"enabled"`
}

func (h *handler) patchAccount(w http.ResponseWriter, r *http.Request) {
	var in accountPatchIn
	if !readJSON(w, r, &in) {
		return
	}
	if in.Options != nil && !validOptions(*in.Options) {
		fail(w, bad("options must be a JSON object"))
		return
	}
	if in.Weight != nil && *in.Weight < 1 {
		fail(w, bad("weight must be >= 1"))
		return
	}
	if in.ProxyURL != nil {
		v := strings.TrimSpace(*in.ProxyURL)
		if !h.validProxyValue(r.Context(), v) {
			fail(w, bad("proxy_url 无效：留空表示继承，direct 表示直连，或填 http/https/socks5/socks5h 代理地址"))
			return
		}
		in.ProxyURL = &v
	}
	var patch store.AccountPatch
	patch.Name, patch.BaseURL, patch.Options = in.Name, in.BaseURL, in.Options
	patch.ProxyURL = in.ProxyURL
	patch.Priority, patch.Weight, patch.Enabled = in.Priority, in.Weight, in.Enabled
	if in.APIKey != nil && *in.APIKey != "" {
		enc, err := h.box.Seal([]byte(*in.APIKey))
		if err != nil {
			fail(w, err)
			return
		}
		patch.APIKeyEnc = &enc
	}
	if err := h.st.UpdateAccount(r.Context(), r.PathValue("id"), patch); err != nil {
		fail(w, err)
		return
	}
	h.getAccount(w, r)
}

func (h *handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := h.st.DeleteAccount(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) resetAccount(w http.ResponseWriter, r *http.Request) {
	if err := h.st.ResetAccount(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	h.getAccount(w, r)
}

type accountOut struct {
	ID            string          `json:"id"`
	ProviderID    string          `json:"provider_id"`
	Name          string          `json:"name"`
	APIKeyMasked  string          `json:"api_key_masked"`
	BaseURL       string          `json:"base_url"`
	ProxyURL      string          `json:"proxy_url"`
	Options       json.RawMessage `json:"options"`
	Priority      int             `json:"priority"`
	Weight        int             `json:"weight"`
	Enabled       bool            `json:"enabled"`
	State         string          `json:"state"`
	CooldownUntil *time.Time      `json:"cooldown_until"`
	FailCount     int64           `json:"fail_count"`
	SuccessCount  int64           `json:"success_count"`
	FailureCount  int64           `json:"failure_count"`
	LastError     string          `json:"last_error"`
	LastErrorAt   *time.Time      `json:"last_error_at"`
	LastUsedAt    *time.Time      `json:"last_used_at"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

func (h *handler) accountOut(a *store.Account) accountOut {
	state := "active"
	if !a.Enabled {
		state = "disabled"
	} else if a.CooldownUntil != nil && a.CooldownUntil.After(h.now()) {
		state = "cooling"
	}
	return accountOut{
		ID: a.ID, ProviderID: a.ProviderID, Name: a.Name,
		APIKeyMasked: h.maskKey(a.APIKeyEnc), BaseURL: a.BaseURL, ProxyURL: a.ProxyURL, Options: a.Options,
		Priority: a.Priority, Weight: a.Weight, Enabled: a.Enabled, State: state,
		CooldownUntil: a.CooldownUntil, FailCount: a.FailCount,
		SuccessCount: a.SuccessCount, FailureCount: a.FailureCount,
		LastError: a.LastError, LastErrorAt: a.LastErrorAt, LastUsedAt: a.LastUsedAt,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
}

func (h *handler) maskKey(enc []byte) string {
	if len(enc) == 0 {
		return ""
	}
	plain, err := h.box.Open(enc)
	if err != nil {
		return "…"
	}
	k := string(plain)
	switch {
	case len(k) <= 4:
		return "…"
	case len(k) <= 8:
		return k[:1] + "…" + k[len(k)-1:]
	default:
		return k[:4] + "…" + k[len(k)-4:]
	}
}

// ---- models ----

func (h *handler) listModels(w http.ResponseWriter, r *http.Request) {
	ms, err := h.st.ListModels(r.Context(), r.URL.Query().Get("provider_id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": ms})
}

func (h *handler) getModel(w http.ResponseWriter, r *http.Request) {
	m, err := h.st.GetModel(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

type modelIn struct {
	ID         string          `json:"id"`
	ProviderID string          `json:"provider_id"`
	Kind       string          `json:"kind"`
	Upstream   string          `json:"upstream"`
	Options    json.RawMessage `json:"options"`
	Enabled    *bool           `json:"enabled"`
}

func (h *handler) createModel(w http.ResponseWriter, r *http.Request) {
	var in modelIn
	if !readJSON(w, r, &in) {
		return
	}
	if !modelIDRE.MatchString(in.ID) {
		fail(w, bad("model id required and must match [a-z0-9][a-z0-9.-]*"))
		return
	}
	if in.ProviderID == "" {
		fail(w, bad("provider_id required"))
		return
	}
	if _, err := h.st.GetProvider(r.Context(), in.ProviderID); err != nil {
		fail(w, bad("provider_id %q: %v", in.ProviderID, err))
		return
	}
	kind := media.Kind(in.Kind)
	if !kind.Valid() {
		fail(w, bad("kind must be image|video|speech"))
		return
	}
	if len(in.Options) > 0 && !validOptions(in.Options) {
		fail(w, bad("options must be a JSON object"))
		return
	}
	if in.Upstream == "" {
		in.Upstream = in.ID
	}
	m := &store.Model{
		ID: in.ID, ProviderID: in.ProviderID, Kind: kind, Upstream: in.Upstream,
		Options: in.Options, Enabled: in.Enabled == nil || *in.Enabled,
	}
	if err := h.st.CreateModel(r.Context(), m); err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, m)
}

type modelPatchIn struct {
	ID         *string          `json:"id"`
	ProviderID *string          `json:"provider_id"`
	Kind       *string          `json:"kind"`
	Upstream   *string          `json:"upstream"`
	Options    *json.RawMessage `json:"options"`
	Enabled    *bool            `json:"enabled"`
}

func (h *handler) patchModel(w http.ResponseWriter, r *http.Request) {
	var in modelPatchIn
	if !readJSON(w, r, &in) {
		return
	}
	oldID := r.PathValue("id")
	newID := oldID
	patch := store.ModelPatch{
		ProviderID: in.ProviderID,
		Upstream:   in.Upstream,
		Options:    in.Options,
		Enabled:    in.Enabled,
	}
	if in.ID != nil {
		if !modelIDRE.MatchString(*in.ID) {
			fail(w, bad("model id required and must match [a-z0-9][a-z0-9.-]*"))
			return
		}
		if *in.ID != oldID {
			patch.ID = in.ID
			newID = *in.ID
		}
	}
	if in.Kind != nil {
		k := media.Kind(*in.Kind)
		if !k.Valid() {
			fail(w, bad("kind must be image|video|speech"))
			return
		}
		patch.Kind = &k
	}
	if in.ProviderID != nil {
		if _, err := h.st.GetProvider(r.Context(), *in.ProviderID); err != nil {
			fail(w, bad("provider_id %q: %v", *in.ProviderID, err))
			return
		}
	}
	if in.Options != nil && !validOptions(*in.Options) {
		fail(w, bad("options must be a JSON object"))
		return
	}
	if err := h.st.UpdateModel(r.Context(), oldID, patch); err != nil {
		fail(w, err)
		return
	}
	m, err := h.st.GetModel(r.Context(), newID)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

func (h *handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	if err := h.st.DeleteModel(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- api keys ----

func (h *handler) listKeys(w http.ResponseWriter, r *http.Request) {
	ks, err := h.st.ListAPIKeys(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": ks})
}

func (h *handler) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	k, plaintext, err := h.st.CreateAPIKey(r.Context(), in.Name, h.box.Seal)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": k.ID, "name": k.Name, "key": plaintext,
	})
}

// keySecret returns the plaintext of a copyable key; pre-key_enc rows kept
// only the hash and can't be recovered.
func (h *handler) keySecret(w http.ResponseWriter, r *http.Request) {
	k, err := h.st.GetAPIKey(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if len(k.KeyEnc) == 0 {
		fail(w, bad("这个密钥是旧版本创建的，只保存了哈希，无法找回，请新建一个"))
		return
	}
	plain, err := h.box.Open(k.KeyEnc)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"key": string(plain)})
}

func (h *handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	if err := h.st.DeleteAPIKey(r.Context(), r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- tasks / logs / status ----

func (h *handler) listTasks(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ts, err := h.st.ListVideoTasks(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tasks": ts})
}

func (h *handler) listLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since time.Time
	if s := q.Get("since"); s != "" {
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
			since = time.UnixMilli(ms)
		} else if t, err := time.Parse(time.RFC3339, s); err == nil {
			since = t
		} else {
			fail(w, bad("since must be unix-ms or RFC3339"))
			return
		}
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	logs, err := h.st.ListRequestLogs(r.Context(), q.Get("account"), since, limit)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	provs, err := h.st.ListProviders(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	type providerStatus struct {
		*store.Provider
		Accounts []accountOut `json:"accounts"`
	}
	out := make([]providerStatus, 0, len(provs))
	for _, p := range provs {
		accs, err := h.st.ListAccounts(r.Context(), p.ID)
		if err != nil {
			fail(w, err)
			return
		}
		ps := providerStatus{Provider: p, Accounts: make([]accountOut, 0, len(accs))}
		for _, a := range accs {
			ps.Accounts = append(ps.Accounts, h.accountOut(a))
		}
		out = append(out, ps)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// ---- settings / proxy test ----

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	proxy, err := h.st.GetSetting(r.Context(), "proxy_url")
	if err != nil {
		fail(w, err)
		return
	}
	dry, err := h.st.GetSetting(r.Context(), dryRunSetting)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"proxy_url": proxy, "dry_run": dry == "1"})
}

// dryRunSetting mirrors openai.DryRunSetting (试运行: generation requests are
// logged but not sent upstream).
const dryRunSetting = "dry_run"

func (h *handler) patchSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProxyURL *string `json:"proxy_url"`
		DryRun   *bool   `json:"dry_run"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.DryRun != nil {
		v := ""
		if *in.DryRun {
			v = "1"
		}
		if err := h.st.SetSetting(r.Context(), dryRunSetting, v); err != nil {
			fail(w, err)
			return
		}
	}
	if in.ProxyURL != nil {
		v := strings.TrimSpace(*in.ProxyURL)
		if !h.validProxyValue(r.Context(), v) {
			fail(w, bad("proxy_url 无效：留空表示跟随系统环境变量，direct 表示直连，或填 http/https/socks5/socks5h 代理地址"))
			return
		}
		if err := h.st.SetSetting(r.Context(), "proxy_url", v); err != nil {
			fail(w, err)
			return
		}
	}
	h.getSettings(w, r)
}

// proxyTest does a GET through the given proxy value ("", "direct" or a URL)
// with a throwaway transport; any HTTP status counts as reachable.
func (h *handler) proxyTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProxyURL string `json:"proxy_url"`
		Target   string `json:"target"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	v := strings.TrimSpace(in.ProxyURL)
	if !h.validProxyValue(r.Context(), v) {
		fail(w, bad("proxy_url 无效：留空表示跟随系统环境变量，direct 表示直连，或填 http/https/socks5/socks5h 代理地址"))
		return
	}
	purl, err := accounts.ResolveProxy(r.Context(), h.st, v)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	target := in.Target
	if target == "" {
		target = "https://queue.fal.run/"
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	switch purl {
	case "":
		tr.Proxy = http.ProxyFromEnvironment
	case "direct":
		tr.Proxy = nil
	default:
		u, _ := url.Parse(purl) // validated above
		tr.Proxy = http.ProxyURL(u)
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	start := h.now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": maskProxyErr(err, purl),
		})
		return
	}
	resp.Body.Close()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"ok": true, "status": resp.StatusCode, "latency_ms": time.Since(start).Milliseconds(),
	})
}

// maskProxyErr scrubs the proxy password out of a transport error message
// (net/http errors echo the proxy URL).
func maskProxyErr(err error, proxyURL string) string {
	msg := err.Error()
	if u, perr := url.Parse(proxyURL); perr == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			msg = strings.ReplaceAll(msg, pw, "***")
			msg = strings.ReplaceAll(msg, url.QueryEscape(pw), "***")
		}
	}
	return msg
}

// ---- proxy pool ----

// proxyOut is the pool entry as the UI needs it: masked URL + usage labels.
func (h *handler) proxyOut(ctx context.Context, p *store.Proxy) (map[string]any, error) {
	used, err := h.st.ProxyUsages(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": p.ID, "name": p.Name, "url": p.URL,
		"url_masked": netproxy.Mask(p.URL), "used_by": used,
		"created_at": p.CreatedAt, "updated_at": p.UpdatedAt,
	}, nil
}

func (h *handler) listProxies(w http.ResponseWriter, r *http.Request) {
	ps, err := h.st.ListProxies(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		o, err := h.proxyOut(r.Context(), p)
		if err != nil {
			fail(w, err)
			return
		}
		out = append(out, o)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"proxies": out})
}

type proxyIn struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func validProxyIn(h *handler, in proxyIn) (*store.Proxy, error) {
	p := &store.Proxy{Name: strings.TrimSpace(in.Name), URL: strings.TrimSpace(in.URL)}
	if p.Name == "" || len(p.Name) > 64 {
		return nil, bad("name 必填且不超过 64 字符")
	}
	if !validProxyScheme(p.URL) {
		return nil, bad("url 必须是 http/https/socks5/socks5h 代理地址")
	}
	return p, nil
}

func (h *handler) createProxy(w http.ResponseWriter, r *http.Request) {
	var in proxyIn
	if !readJSON(w, r, &in) {
		return
	}
	p, err := validProxyIn(h, in)
	if err != nil {
		fail(w, err)
		return
	}
	if err := h.st.CreateProxy(r.Context(), p); err != nil {
		fail(w, err)
		return
	}
	o, err := h.proxyOut(r.Context(), p)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, o)
}

type proxyPatchIn struct {
	Name *string `json:"name"`
	URL  *string `json:"url"`
}

func (h *handler) patchProxy(w http.ResponseWriter, r *http.Request) {
	var in proxyPatchIn
	if !readJSON(w, r, &in) {
		return
	}
	if in.Name != nil {
		v := strings.TrimSpace(*in.Name)
		if v == "" || len(v) > 64 {
			fail(w, bad("name 必填且不超过 64 字符"))
			return
		}
		in.Name = &v
	}
	if in.URL != nil {
		v := strings.TrimSpace(*in.URL)
		if !validProxyScheme(v) {
			fail(w, bad("url 必须是 http/https/socks5/socks5h 代理地址"))
			return
		}
		in.URL = &v
	}
	id := r.PathValue("id")
	if err := h.st.UpdateProxy(r.Context(), id, in.Name, in.URL); err != nil {
		fail(w, err)
		return
	}
	p, err := h.st.GetProxy(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	o, err := h.proxyOut(r.Context(), p)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, o)
}

// deleteProxy refuses while anything still references the pool entry.
func (h *handler) deleteProxy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	used, err := h.st.ProxyUsages(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if len(used) > 0 {
		httpx.Error(w, http.StatusConflict, "该代理正在被使用："+strings.Join(used, "、"), "conflict", "conflict")
		return
	}
	if err := h.st.DeleteProxy(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
