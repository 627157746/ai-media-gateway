package openai

// Deriving a model's image capabilities from the vendor's endpoint schema.
// The schema is cached because GET /v1/models is a hot, client-polled endpoint
// while a vendor schema only changes when the model itself is replaced.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"ai-media-gateway/internal/accounts"
	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
	"ai-media-gateway/internal/store"
)

// schemaTTL bounds how stale a declared capability can be. Long enough to keep
// /v1/models cheap, short enough that a vendor changing a model is picked up in
// the same session.
const schemaTTL = 30 * time.Minute

// schemaFailTTL is how long a failed lookup is remembered. Much shorter than
// schemaTTL: a transient vendor outage must not hide a model's capabilities for
// half an hour.
const schemaFailTTL = 2 * time.Minute

type schemaCacheEntry struct {
	schema  *provider.EndpointSchema
	fetched time.Time
	// failed records a miss so a broken endpoint is not retried on every call.
	failed bool
}

var (
	schemaCacheMu sync.Mutex
	schemaCache   = map[string]schemaCacheEntry{}
)

// resetSchemaCache drops every cached schema. For tests.
func resetSchemaCache() {
	schemaCacheMu.Lock()
	schemaCache = map[string]schemaCacheEntry{}
	schemaCacheMu.Unlock()
}

// imageSchema resolves an image model's endpoint schema, or nil when the vendor
// publishes none or the lookup fails. A nil result makes the caller fall back to
// the hand-declared table, so a schema outage only degrades the detail of
// /v1/models instead of breaking it.
//
// A model's OWN endpoint decides what is mandatory. When its options name an
// `upstream_edit` sibling, that sibling's reference-image fields are merged in
// as optional: editing becomes possible, but a text-to-image model stays usable
// without images.
func (h *handler) imageSchema(ctx context.Context, m *store.Model, prov *store.Provider) *provider.EndpointSchema {
	own, _ := h.loadSchema(ctx, prov, m, m.Upstream)
	if own == nil {
		return nil
	}
	if edit := upstreamEditFromOptions(m.Options); edit != "" {
		if es, _ := h.loadSchema(ctx, prov, m, edit); es != nil {
			mergeEditFields(own, es)
		}
	}
	return own
}

// loadSchema fetches one endpoint's schema, cached for schemaTTL. A failed
// lookup is cached too so an unreachable endpoint is not retried per request.
func (h *handler) loadSchema(ctx context.Context, prov *store.Provider, m *store.Model, endpoint string) (*provider.EndpointSchema, error) {
	return h.loadSchemaFor(ctx, prov, m, endpoint, m.Kind)
}

// loadSchemaFor is loadSchema with an explicit kind, used by the models
// endpoint before it knows which capability family to describe.
func (h *handler) loadSchemaFor(ctx context.Context, prov *store.Provider, m *store.Model, endpoint string, kind media.Kind) (*provider.EndpointSchema, error) {
	adapter, ok := provider.Get(prov.Type)
	if !ok {
		return nil, nil
	}
	lister, ok := adapter.(provider.SchemaLister)
	if !ok {
		return nil, nil
	}

	key := prov.ID + "\x00" + endpoint
	schemaCacheMu.Lock()
	if e, ok := schemaCache[key]; ok && e.fresh(time.Now()) {
		schemaCacheMu.Unlock()
		if e.failed {
			return nil, nil
		}
		return cloneSchema(e.schema), nil
	}
	schemaCacheMu.Unlock()

	res, err := accounts.Resolve(ctx, h.st, m.ID, kind)
	if err != nil {
		h.cacheSchema(key, nil, true)
		return nil, err
	}
	// NOT pool.Do: a schema read is not a generation, so it must neither cool
	// an account down on a docs-host hiccup nor RecordSuccess (which clears the
	// cooldown and fail_count of an account that is failing real traffic).
	call, err := h.schemaCall(ctx, res)
	if err != nil {
		h.cacheSchema(key, nil, true)
		return nil, nil
	}
	// Bounded so a slow vendor cannot stall GET /v1/models.
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	s, err := lister.EndpointSchema(cctx, call, endpoint)
	if err != nil {
		// A client that hung up says nothing about the endpoint: do not let it
		// poison the cache for everyone else.
		if ctx.Err() == nil {
			h.cacheSchema(key, nil, true)
		}
		return nil, nil
	}
	h.cacheSchema(key, cloneSchema(&s), false)
	return &s, nil
}

// cloneSchema copies the Fields slice so a caller's merge (which appends fields
// and narrows enums in place) never reaches the shared cache entry. Without it
// every /v1/models call mutated the cached schema concurrently, and a model
// without upstream_edit that shares the endpoint inherited its sibling's
// image_urls.
func cloneSchema(s *provider.EndpointSchema) *provider.EndpointSchema {
	if s == nil {
		return nil
	}
	cp := *s
	cp.Fields = append([]provider.EndpointField(nil), s.Fields...)
	return &cp
}

func (e schemaCacheEntry) fresh(now time.Time) bool {
	ttl := schemaTTL
	if e.failed {
		ttl = schemaFailTTL
	}
	return now.Sub(e.fetched) < ttl
}

// schemaCall builds a Call for a schema read: the first enabled keyed account
// the model may use (fal raises rate limits for keyed reads), else an anonymous
// call through the global proxy, since the schema host serves those too.
func (h *handler) schemaCall(ctx context.Context, res accounts.Resolved) (provider.Call, error) {
	accs, err := h.st.ListAccounts(ctx, res.Provider.ID)
	if err != nil {
		return provider.Call{}, err
	}
	allowed := map[string]bool{}
	for _, id := range res.AllowedAccounts {
		allowed[id] = true
	}
	// Keyed accounts first, then keyless ones; either way the account's own
	// proxy applies.
	for _, keyed := range []bool{true, false} {
		for _, a := range accs {
			if !a.Enabled || (len(a.APIKeyEnc) > 0) != keyed || (len(allowed) > 0 && !allowed[a.ID]) {
				continue
			}
			if c, err := h.pool.CallFor(ctx, res, a.ID); err == nil {
				return c, nil
			}
		}
	}
	client, err := h.pool.StreamForGlobal(ctx)
	if err != nil {
		return provider.Call{}, err
	}
	return provider.Call{Provider: res.Provider, Model: res.Model, HTTP: client, Stream: client}, nil
}

// videoSchema resolves a video model's schema, merging the reference sibling the
// same way images merge upstream_edit. MiniMax's reference-to-video is a superset
// of image-to-video (it adds the reference_* fields), so one model can declare
// both modes instead of forcing a second model entry.
func (h *handler) videoSchema(ctx context.Context, m *store.Model, prov *store.Provider) *provider.EndpointSchema {
	own, _ := h.loadSchemaFor(ctx, prov, m, m.Upstream, media.KindVideo)
	if own == nil {
		return nil
	}
	if ref := upstreamReferenceFromOptions(m.Options); ref != "" {
		if rs, _ := h.loadSchemaFor(ctx, prov, m, ref, media.KindVideo); rs != nil {
			mergeVideoFields(own, rs)
		}
	}
	return own
}

// mergeVideoFields adds the reference-media fields of a reference-to-video
// sibling, narrowing shared enums to what both endpoints accept so a value is
// never offered that only one of them supports.
func mergeVideoFields(dst, ref *provider.EndpointSchema) {
	// Narrow shared enum fields to what BOTH endpoints accept.
	for i := range dst.Fields {
		f := &dst.Fields[i]
		if len(f.Enum) == 0 {
			continue
		}
		rf, ok := ref.Field(f.Name)
		if !ok || len(rf.Enum) == 0 {
			continue
		}
		if narrowed := intersect(f.Enum, rf.Enum); len(narrowed) > 0 {
			f.Enum = narrowed
		}
	}
	// Add every field the reference endpoint has and the model's own endpoint
	// lacks. It is not only the reference_* family: MiniMax's image-to-video has
	// no aspect_ratio at all while its reference-to-video does, so limiting the
	// merge to reference_* hid a control the model can actually use.
	for _, rf := range ref.Fields {
		if _, exists := dst.Field(rf.Name); exists {
			continue
		}
		// A field the primary endpoint does not declare is only reachable in
		// reference mode, so it must not be advertised as required.
		rf.Required = false
		dst.Fields = append(dst.Fields, rf)
	}
}

// upstreamReferenceFromOptions reads options.upstream_reference, the model
// option that makes the fal adapter swap endpoints for reference mode.
func upstreamReferenceFromOptions(opts []byte) string {
	if len(opts) == 0 {
		return ""
	}
	var o struct {
		UpstreamReference string `json:"upstream_reference"`
	}
	if json.Unmarshal(opts, &o) != nil {
		return ""
	}
	return o.UpstreamReference
}

// mergeEditFields widens a text-to-image model's schema to also cover its edit
// sibling, so one declaration describes both paths.
//
// Two rules matter:
//   - Reference-image fields are added as OPTIONAL: the model's own endpoint
//     does not demand them, so text-to-image keeps working.
//   - Every field BOTH endpoints accept must be narrowed to the intersection.
//     A model advertising svg because its t2i endpoint has it would fail as soon
//     as the user attaches an image and the request is repointed at the edit
//     endpoint, which does not accept svg.
func mergeEditFields(dst, edit *provider.EndpointSchema) {
	// Narrow shared enum fields to what both endpoints accept.
	for i := range dst.Fields {
		f := &dst.Fields[i]
		if len(f.Enum) == 0 {
			continue
		}
		ef, ok := edit.Field(f.Name)
		if !ok || len(ef.Enum) == 0 {
			// The edit endpoint does not document this field with an enum, so
			// nothing can be guaranteed about it: leave the model's own list.
			continue
		}
		if narrowed := intersect(f.Enum, ef.Enum); len(narrowed) > 0 {
			f.Enum = narrowed
		}
	}

	// Reference-image fields only exist on the edit endpoint.
	for _, name := range []string{"image_urls", "mask_url"} {
		f, ok := edit.Field(name)
		if !ok {
			continue
		}
		if _, exists := dst.Field(name); exists {
			continue
		}
		f.Required = false
		dst.Fields = append(dst.Fields, f)
	}
}

// intersect returns the values present in both lists, preserving a's order so a
// client keeps a stable option sequence.
func intersect(a, b []string) []string {
	out := make([]string, 0, len(a))
	for _, v := range a {
		for _, w := range b {
			if strings.EqualFold(v, w) {
				out = append(out, v)
				break
			}
		}
	}
	return out
}

func (h *handler) cacheSchema(key string, s *provider.EndpointSchema, failed bool) {
	schemaCacheMu.Lock()
	schemaCache[key] = schemaCacheEntry{schema: s, fetched: time.Now(), failed: failed}
	schemaCacheMu.Unlock()
}

// upstreamEditFromOptions reads options.upstream_edit, the model option that
// makes the fal adapter swap endpoints when reference images are supplied.
func upstreamEditFromOptions(opts []byte) string {
	if len(opts) == 0 {
		return ""
	}
	var o struct {
		UpstreamEdit string `json:"upstream_edit"`
	}
	if json.Unmarshal(opts, &o) != nil {
		return ""
	}
	return o.UpstreamEdit
}
