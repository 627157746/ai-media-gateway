package server

import (
	"net/http"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/config"
	"ai-media-gateway/internal/httpx"
	"ai-media-gateway/internal/store"
)

// Deps are the wiring inputs for the HTTP tree.
type Deps struct {
	Config config.Config
	Store  *store.Store
	Pool   *accounts.Pool
	Admin  http.Handler
	OpenAI http.Handler // mounted under /v1/ behind gateway auth
	Web    http.Handler // admin UI, mounted at / without auth
}

// New builds the root handler: recovery → request id → CORS → request log →
// body limit → routes (per-subtree auth).
func New(d Deps) http.Handler {
	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", healthz)
	if d.OpenAI != nil {
		root.Handle("/v1/", gatewayAuth(d.Store, d.Config.AdminToken, d.OpenAI))
	}
	if d.Admin != nil {
		root.Handle("/admin/", adminAuth(d.Config.AdminToken, d.Admin))
	}
	if d.Web != nil {
		root.Handle("/", d.Web)
	}

	var h http.Handler = root
	h = bodyLimit(h, d.Config.MaxBodyBytes)
	h = requestLog(d.Store, h)
	h = cors(h, d.Config.CORSOrigins)
	h = requestID(h)
	h = recoverer(h)
	return h
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
