package fal

// fal model catalog: GET {catalogBase}/models — separate host from the
// queue API. Auth is optional; a Key header raises rate limits.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

// var for tests.
var catalogBase = "https://api.fal.ai/v1"

// SetCatalogBase overrides the catalog endpoint; returns a restore func.
// For tests.
func SetCatalogBase(u string) func() {
	old := catalogBase
	catalogBase = strings.TrimSuffix(u, "/")
	return func() { catalogBase = old }
}

// catalogKinds maps fal categories to importable kinds; the rest are dropped.
var catalogKinds = map[string]media.Kind{
	"text-to-image":  media.KindImage,
	"image-to-image": media.KindImage,
	"text-to-video":  media.KindVideo,
	"image-to-video": media.KindVideo,
	"text-to-speech": media.KindSpeech,
}

type catalogEntry struct {
	EndpointID string `json:"endpoint_id"`
	Metadata   struct {
		DisplayName string `json:"display_name"`
		Category    string `json:"category"`
		Description string `json:"description"`
		Status      string `json:"status"`
	} `json:"metadata"`
}

type catalogPage struct {
	Models     []catalogEntry `json:"models"`
	NextCursor *string        `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}

func (Adapter) ListModels(ctx context.Context, c provider.Call, q provider.ModelQuery) (provider.ModelPage, error) {
	if len(q.IDs) > 0 {
		return listByIDs(ctx, c, q.IDs)
	}
	qs := url.Values{}
	qs.Set("limit", "50")
	qs.Set("status", "active")
	if q.Q != "" {
		qs.Set("q", q.Q)
	}
	if q.Category != "" {
		qs.Set("category", q.Category)
	}
	if q.Cursor != "" {
		qs.Set("cursor", q.Cursor)
	}
	page, err := fetchCatalog(ctx, c, qs)
	if err != nil {
		return provider.ModelPage{}, err
	}
	out := provider.ModelPage{Models: []provider.UpstreamModel{}}
	if page.NextCursor != nil {
		out.NextCursor = *page.NextCursor
	}
	for _, e := range page.Models {
		if m, ok := mapEntry(e); ok {
			out.Models = append(out.Models, m)
		}
	}
	return out, nil
}

// fal's find mode is all-or-nothing: a multi-endpoint_id request 404s
// when any id is unknown. Each id therefore gets its own request
// (≤8 concurrent); a 404 means "doesn't exist" and the id is skipped.
func listByIDs(ctx context.Context, c provider.Call, ids []string) (provider.ModelPage, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		m   provider.UpstreamModel
		ok  bool
		err error
	}
	const maxConc = 8
	sem := make(chan struct{}, maxConc)
	results := make([]result, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			m, ok, err := lookupID(ctx, c, id)
			if err != nil && ctx.Err() != nil {
				// Another request already aborted the group: this error is
				// a cancellation artifact (possibly wrapped), not a real one.
				err = ctx.Err()
			}
			results[i] = result{m: m, ok: ok, err: err}
			if err != nil {
				cancel()
			}
		}(i, id)
	}
	wg.Wait()
	// Prefer a real error over ctx errors caused by the abort.
	var firstErr, ctxErr error
	for _, r := range results {
		if r.err == nil {
			continue
		}
		if errors.Is(r.err, context.Canceled) || errors.Is(r.err, context.DeadlineExceeded) {
			if ctxErr == nil {
				ctxErr = r.err
			}
			continue
		}
		if firstErr == nil {
			firstErr = r.err
		}
	}
	if firstErr == nil {
		firstErr = ctxErr
	}
	if firstErr != nil {
		return provider.ModelPage{}, firstErr
	}
	out := provider.ModelPage{Models: []provider.UpstreamModel{}}
	for _, r := range results {
		if r.ok {
			out.Models = append(out.Models, r.m)
		}
	}
	return out, nil
}

func lookupID(ctx context.Context, c provider.Call, id string) (m provider.UpstreamModel, ok bool, err error) {
	qs := url.Values{}
	qs.Set("limit", "50")
	qs.Set("status", "active")
	qs.Set("endpoint_id", id)
	page, err := fetchCatalog(ctx, c, qs)
	if err != nil {
		var ue *media.UpstreamError
		if errors.As(err, &ue) && ue.Status == http.StatusNotFound {
			return provider.UpstreamModel{}, false, nil
		}
		return provider.UpstreamModel{}, false, err
	}
	for _, e := range page.Models {
		if m, ok = mapEntry(e); ok {
			return m, true, nil
		}
	}
	return provider.UpstreamModel{}, false, nil
}

func fetchCatalog(ctx context.Context, c provider.Call, qs url.Values) (*catalogPage, error) {
	u, err := url.Parse(catalogBase + "/models")
	if err != nil {
		return nil, err
	}
	u.RawQuery = qs.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.Account.APIKey != "" {
		req.Header.Set("Authorization", "Key "+c.Account.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, provider.TransportError(err, false)
	}
	if resp.StatusCode/100 != 2 {
		return nil, statusError(resp)
	}
	var page catalogPage
	err = json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if err != nil {
		return nil, &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad catalog response: " + err.Error(),
		}
	}
	return &page, nil
}

func mapEntry(e catalogEntry) (provider.UpstreamModel, bool) {
	kind, ok := catalogKinds[e.Metadata.Category]
	if !ok {
		return provider.UpstreamModel{}, false
	}
	name := e.Metadata.DisplayName
	if name == "" {
		name = e.EndpointID
	}
	return provider.UpstreamModel{
		ID:          e.EndpointID,
		Name:        name,
		Kind:        kind,
		Category:    e.Metadata.Category,
		Description: e.Metadata.Description,
	}, true
}
