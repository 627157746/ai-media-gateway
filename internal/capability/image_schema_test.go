package capability

import (
	"encoding/json"
	"testing"

	"ai-media-gateway/internal/provider"
)

func schema(fields ...provider.EndpointField) *provider.EndpointSchema {
	return &provider.EndpointSchema{Endpoint: "test", Fields: fields}
}

func num(v int) *int { return &v }

// TestImageFromSchemaT2I derives a text-to-image endpoint: no image_urls, so no
// edit support, and num_images carries its documented ceiling.
func TestImageFromSchemaT2I(t *testing.T) {
	s := schema(
		provider.EndpointField{Name: "prompt", Required: true},
		provider.EndpointField{Name: "image_size", Enum: []string{"square_hd", "auto"}},
		provider.EndpointField{Name: "num_images", Maximum: num(10)},
		provider.EndpointField{Name: "quality", Enum: []string{"low", "high"}},
		provider.EndpointField{Name: "output_format", Enum: []string{"png", "svg"}},
	)
	got := ImageFromSchema(*s)

	if got.SupportsEdit || got.RequiresImages {
		t.Fatalf("no image_urls means no editing: %+v", got)
	}
	if got.MaxN != 10 {
		t.Fatalf("max_n: %d", got.MaxN)
	}
	if got.SizeMode != "image_size" || len(got.Sizes) != 2 {
		t.Fatalf("size: %+v", got)
	}
	if len(got.Qualities) != 2 || len(got.Formats) != 2 {
		t.Fatalf("enums: %+v", got)
	}
}

// TestImageFromSchemaEdit derives an image-to-image endpoint: image_urls
// present and required, with its array cap and the mask field.
func TestImageFromSchemaEdit(t *testing.T) {
	s := schema(
		provider.EndpointField{Name: "prompt", Required: true},
		provider.EndpointField{Name: "image_urls", Required: true, MaxItems: num(16)},
		provider.EndpointField{Name: "mask_url"},
		provider.EndpointField{Name: "num_images", Maximum: num(4)},
	)
	got := ImageFromSchema(*s)

	if !got.SupportsEdit || !got.RequiresImages {
		t.Fatalf("edit endpoint must require images: %+v", got)
	}
	if !got.SupportsMask {
		t.Fatalf("mask_url present means mask support: %+v", got)
	}
	if got.MaxEditImages != 16 {
		t.Fatalf("max_edit_images: %d", got.MaxEditImages)
	}
	if got.MaxN != 4 {
		t.Fatalf("max_n: %d", got.MaxN)
	}
}

// TestImageFromSchemaAspectRatio covers vendors that size output by ratio, as
// fal's nano-banana does; the field name has to reach the adapter so it is not
// forwarded as image_size.
func TestImageFromSchemaAspectRatio(t *testing.T) {
	s := schema(
		provider.EndpointField{Name: "prompt", Required: true},
		provider.EndpointField{Name: "aspect_ratio", Enum: []string{"1:1", "16:9", "auto"}},
	)
	got := ImageFromSchema(*s)
	if got.SizeMode != "aspect_ratio" {
		t.Fatalf("size mode: %q", got.SizeMode)
	}
	if len(got.Sizes) != 3 {
		t.Fatalf("sizes: %v", got.Sizes)
	}
}

// TestImageFromSchemaOmitsAbsentFields is the anti-invention rule: a field the
// schema does not list must never be advertised, because the client would then
// offer an input the endpoint rejects.
func TestImageFromSchemaOmitsAbsentFields(t *testing.T) {
	s := schema(provider.EndpointField{Name: "prompt", Required: true})
	got := ImageFromSchema(*s)

	if len(got.Qualities) != 0 || len(got.Backgrounds) != 0 || len(got.Formats) != 0 {
		t.Fatalf("invented options: %+v", got)
	}
	if len(got.Sizes) != 0 || got.MaxN != 0 {
		t.Fatalf("invented size/count: %+v", got)
	}
}

// TestImageFromSchemaForKindsPrefersSchema checks the fallback order: a live
// schema wins, and a missing one falls back to the hand-declared table.
func TestImageFromSchemaForKindsPrefersSchema(t *testing.T) {
	s := schema(provider.EndpointField{Name: "num_images", Maximum: num(7)})
	got := ImageFromSchemaForKinds(s, "fal", "openai/gpt-image-2.6/sunburst/edit", nil)
	if got.MaxN != 7 {
		t.Fatalf("schema should win: %+v", got)
	}

	fallback := ImageFromSchemaForKinds(nil, "fal", "openai/gpt-image-2.6/sunburst/edit", nil)
	if fallback.MaxN != 10 {
		t.Fatalf("nil schema should fall back to the declared table: %+v", fallback)
	}

	// Options still win over the derived spec.
	opts, _ := json.Marshal(map[string]any{"max_n": 2})
	over := ImageFromSchemaForKinds(s, "fal", "x/edit", opts)
	if over.MaxN != 2 {
		t.Fatalf("options should override the schema: %+v", over)
	}
}

// TestIsEditEndpoint pins the /edit suffix rule used by the declared-table path.
func TestIsEditEndpoint(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"openai/gpt-image-2.6/sunburst/edit", true},
		{"openai/gpt-image-2.6/sunburst/edit/", true},
		{"openai/gpt-image-2.6/sunburst/text-to-image", false},
		{"", false},
	} {
		if got := IsEditEndpoint(c.in); got != c.want {
			t.Fatalf("IsEditEndpoint(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
