package fal

// fal publishes an OpenAPI document per endpoint. Reading it lets the gateway
// derive a model's capabilities from the vendor's own schema instead of
// transcribing parameter lists into Go by hand, which went stale the moment a
// new model was added.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"ai-media-gateway/internal/media"
	"ai-media-gateway/internal/provider"
)

// var for tests.
var openapiBase = "https://fal.ai/api/openapi/queue/openapi.json"

// SetOpenAPIBase overrides the OpenAPI host; returns a restore func. For tests.
func SetOpenAPIBase(u string) func() {
	old := openapiBase
	openapiBase = strings.TrimSuffix(u, "/")
	return func() { openapiBase = old }
}

// openapiDoc is the subset of the OpenAPI document we read.
type openapiDoc struct {
	Components struct {
		Schemas map[string]openapiSchema `json:"schemas"`
	} `json:"components"`
}

type openapiSchema struct {
	Properties map[string]openapiProp `json:"properties"`
	Required   []string               `json:"required"`
}

type openapiProp struct {
	Type     string          `json:"type"`
	Enum     []string        `json:"enum"`
	AnyOf    []openapiProp   `json:"anyOf"`
	Maximum  *float64        `json:"maximum"`
	Minimum  *float64        `json:"minimum"`
	MinItems *int            `json:"minItems"`
	MaxItems *int            `json:"maxItems"`
	Default  json.RawMessage `json:"default"`
	Ref      string          `json:"$ref"`
	Desc     string          `json:"description"`
}

// EndpointSchema fetches an endpoint's OpenAPI document and reduces it to the
// input fields the gateway cares about. fal serves this anonymously, so a
// keyless provider still works.
func (Adapter) EndpointSchema(ctx context.Context, c provider.Call, endpoint string) (provider.EndpointSchema, error) {
	u, err := url.Parse(openapiBase)
	if err != nil {
		return provider.EndpointSchema{}, err
	}
	qs := u.Query()
	qs.Set("endpoint_id", endpoint)
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return provider.EndpointSchema{}, err
	}
	if c.Account.APIKey != "" {
		req.Header.Set("Authorization", "Key "+c.Account.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return provider.EndpointSchema{}, provider.TransportError(err, false)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return provider.EndpointSchema{}, statusError(resp)
	}
	var doc openapiDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return provider.EndpointSchema{}, &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "bad openapi response: " + err.Error(),
		}
	}

	// The input model is the schema named *Input. fal serves exactly one per
	// endpoint today, but map iteration order is random, so pick deterministically:
	// if several ever appear, the alphabetically first wins instead of a random
	// one, and a mismatch cannot flip a model's declared capabilities between
	// requests (the result is cached for 30 minutes).
	var candidates []string
	for name := range doc.Components.Schemas {
		if strings.HasSuffix(name, "Input") {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	var in *openapiSchema
	if len(candidates) > 0 {
		cp := doc.Components.Schemas[candidates[0]]
		in = &cp
	}
	if in == nil {
		return provider.EndpointSchema{}, &media.UpstreamError{
			Class: media.ClassUpstream, Status: resp.StatusCode,
			Message: "openapi document has no *Input schema for " + endpoint,
		}
	}

	required := make(map[string]bool, len(in.Required))
	for _, r := range in.Required {
		required[r] = true
	}

	out := provider.EndpointSchema{Endpoint: endpoint, Fields: []provider.EndpointField{}}
	// Deterministic order so responses (and tests) are stable.
	for _, name := range sortedKeys(in.Properties) {
		p := in.Properties[name]
		f := provider.EndpointField{Name: name, Required: required[name], Description: p.Desc}
		f.Enum = propEnum(p)
		if p.Maximum != nil {
			if v := int(*p.Maximum); float64(v) == *p.Maximum {
				f.Maximum = &v
			}
		}
		if p.Minimum != nil {
			if v := int(*p.Minimum); float64(v) == *p.Minimum {
				f.Minimum = &v
			}
		}
		// Ranged integers put minimum/maximum inside an anyOf branch
		// (integer-or-null), so read them from there too.
		for _, a := range p.AnyOf {
			if f.Minimum == nil && a.Minimum != nil {
				if v := int(*a.Minimum); float64(v) == *a.Minimum {
					f.Minimum = &v
				}
			}
			if f.Maximum == nil && a.Maximum != nil {
				if v := int(*a.Maximum); float64(v) == *a.Maximum {
					f.Maximum = &v
				}
			}
		}
		f.MaxItems = p.MaxItems
		if len(p.Default) > 0 && string(p.Default) != "null" {
			var v any
			if json.Unmarshal(p.Default, &v) == nil {
				f.Default = v
			}
		}
		out.Fields = append(out.Fields, f)
	}
	return out, nil
}

// propEnum flattens the enum, which fal puts either directly on the property or
// inside one branch of an anyOf (a nullable enum, or enum-or-object).
func propEnum(p openapiProp) []string {
	if len(p.Enum) > 0 {
		return p.Enum
	}
	for _, a := range p.AnyOf {
		if len(a.Enum) > 0 {
			return a.Enum
		}
	}
	return nil
}

func sortedKeys(m map[string]openapiProp) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Insertion sort keeps this dependency-free and the sets are tiny.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
