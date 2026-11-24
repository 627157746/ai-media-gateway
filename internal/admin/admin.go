package admin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/secret"
	"ai-media-gateway/internal/store"
)

var idRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// modelIDRE also allows dots (e.g. seedance-2.5) but not a leading one, so
// "." / ".." can never become a path segment under /admin/models/.
var modelIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

type handler struct {
	st  *store.Store
	box *secret.Box
	now func() time.Time
}

// New returns the admin API handler (already mounted under /admin/).
func New(st *store.Store, box *secret.Box) http.Handler {
	h := &handler{st: st, box: box, now: time.Now}
	m := http.NewServeMux()
	m.HandleFunc("GET /admin/providers", h.listProviders)
	m.HandleFunc("POST /admin/providers", h.createProvider)
	m.HandleFunc("GET /admin/providers/{id}", h.getProvider)
	m.HandleFunc("PATCH /admin/providers/{id}", h.patchProvider)
	m.HandleFunc("DELETE /admin/providers/{id}", h.deleteProvider)
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
	m.HandleFunc("DELETE /admin/keys/{id}", h.deleteKey)
	m.HandleFunc("GET /admin/tasks", h.listTasks)
	m.HandleFunc("GET /admin/logs", h.listLogs)
	m.HandleFunc("GET /admin/status", h.status)
	m.HandleFunc("GET /admin/provider-types", h.providerTypes)
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
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	BaseURL string          `json:"base_url"`
	Options json.RawMessage `json:"options"`
	Enabled *bool           `json:"enabled"`
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
	p := &store.Provider{
		ID:      in.ID,
		Type:    in.Type,
		BaseURL: in.BaseURL,
		Options: in.Options,
		Enabled: in.Enabled == nil || *in.Enabled,
	}
	if err := h.st.CreateProvider(r.Context(), p); err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, p)
}

type providerPatchIn struct {
	Type    *string          `json:"type"`
	BaseURL *string          `json:"base_url"`
	Options *json.RawMessage `json:"options"`
	Enabled *bool            `json:"enabled"`
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
	if err := h.st.UpdateProvider(r.Context(), r.PathValue("id"), store.ProviderPatch{
		Type:    in.Type,
		BaseURL: in.BaseURL,
		Options: in.Options,
		Enabled: in.Enabled,
	}); err != nil {
		fail(w, err)
		return
	}
	h.getProvider(w, r)
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
	var patch store.AccountPatch
	patch.Name, patch.BaseURL, patch.Options = in.Name, in.BaseURL, in.Options
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
		APIKeyMasked: h.maskKey(a.APIKeyEnc), BaseURL: a.BaseURL, Options: a.Options,
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
	patch := store.ModelPatch{
		ProviderID: in.ProviderID,
		Upstream:   in.Upstream,
		Options:    in.Options,
		Enabled:    in.Enabled,
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
	if err := h.st.UpdateModel(r.Context(), r.PathValue("id"), patch); err != nil {
		fail(w, err)
		return
	}
	h.getModel(w, r)
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
	k, plaintext, err := h.st.CreateAPIKey(r.Context(), in.Name)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": k.ID, "name": k.Name, "key": plaintext,
	})
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
