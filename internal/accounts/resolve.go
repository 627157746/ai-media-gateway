package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/store"
)

// Resolve maps a requested model id to a provider + upstream model.
//  1. Exact enabled models.id alias → kind must match (ErrWrongKind).
//  2. "<providerID>/<rest>" on an enabled provider with passthrough on
//     (default) → synthetic model with upstream=rest.
//  3. ErrModelNotFound.
func Resolve(ctx context.Context, st *store.Store, modelID string, kind media.Kind) (Resolved, error) {
	if m, err := st.GetModel(ctx, modelID); err == nil && m.Enabled {
		prov, err := enabledProvider(ctx, st, m.ProviderID, modelID)
		if err != nil {
			return Resolved{}, err
		}
		if m.Kind != kind {
			return Resolved{}, fmt.Errorf("%w: model %q is %s, not %s", media.ErrWrongKind, modelID, m.Kind, kind)
		}
		return Resolved{
			Provider:        providerConfig(prov),
			Model:           provider.ModelConfig{ID: m.ID, Kind: m.Kind, Upstream: m.Upstream, Options: m.Options},
			AllowedAccounts: accountAllowlist(m.Options),
		}, nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Resolved{}, err
	}

	if i := strings.IndexByte(modelID, '/'); i > 0 && i < len(modelID)-1 {
		provID, rest := modelID[:i], modelID[i+1:]
		if prov, err := st.GetProvider(ctx, provID); err == nil && prov.Enabled && passthroughOn(prov.Options) {
			return Resolved{
				Provider: providerConfig(prov),
				Model:    provider.ModelConfig{ID: modelID, Kind: kind, Upstream: rest},
			}, nil
		}
	}
	return Resolved{}, fmt.Errorf("%w: %s", media.ErrModelNotFound, modelID)
}

func enabledProvider(ctx context.Context, st *store.Store, id, modelID string) (*store.Provider, error) {
	prov, err := st.GetProvider(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", media.ErrModelNotFound, modelID)
	}
	if err != nil {
		return nil, err
	}
	if !prov.Enabled {
		return nil, fmt.Errorf("%w: %s", media.ErrModelNotFound, modelID)
	}
	return prov, nil
}

func providerConfig(p *store.Provider) provider.ProviderConfig {
	return provider.ProviderConfig{ID: p.ID, Type: p.Type, BaseURL: p.BaseURL, Options: p.Options}
}

func passthroughOn(opts json.RawMessage) bool {
	var o struct {
		Passthrough *bool `json:"passthrough"`
	}
	if len(opts) > 0 {
		_ = json.Unmarshal(opts, &o)
	}
	return o.Passthrough == nil || *o.Passthrough
}

func accountAllowlist(opts json.RawMessage) []string {
	var o struct {
		Accounts []string `json:"accounts"`
	}
	if len(opts) > 0 {
		_ = json.Unmarshal(opts, &o)
	}
	return o.Accounts
}
