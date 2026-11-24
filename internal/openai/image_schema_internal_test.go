package openai

import (
	"testing"
	"time"

	"ai-media-gateway/internal/provider"
)

func field(name string, required bool, maxItems int) provider.EndpointField {
	f := provider.EndpointField{Name: name, Required: required}
	if maxItems > 0 {
		v := maxItems
		f.MaxItems = &v
	}
	return f
}

// TestMergeEditFieldsKeepsImagesOptional is the regression guard for a real bug:
// a text-to-image model whose options name an `upstream_edit` sibling was
// advertised with requires_images=true, which told clients the model could only
// edit and blocked plain text-to-image generation.
func TestMergeEditFieldsKeepsImagesOptional(t *testing.T) {
	own := &provider.EndpointSchema{
		Endpoint: "krea/v3/text-to-image",
		Fields: []provider.EndpointField{
			field("prompt", true, 0),
			field("image_size", false, 0),
		},
	}
	edit := &provider.EndpointSchema{
		Endpoint: "krea/v3/edit",
		Fields: []provider.EndpointField{
			field("image_urls", true, 8),
			field("mask_url", false, 0),
		},
	}

	mergeEditFields(own, edit)

	for _, name := range []string{"image_urls", "mask_url"} {
		f, ok := own.Field(name)
		if !ok {
			t.Fatalf("%s should be merged in", name)
		}
		if f.Required {
			t.Fatalf("%s must be optional on a text-to-image model", name)
		}
	}
	if f, _ := own.Field("image_urls"); f.MaxItems == nil || *f.MaxItems != 8 {
		t.Fatalf("maxItems should survive the merge: %+v", f)
	}
	// The model's own fields stay untouched.
	if f, ok := own.Field("prompt"); !ok || !f.Required {
		t.Fatalf("prompt must stay required: %+v", f)
	}
}

// TestMergeEditFieldsDoesNotOverwrite guards against the edit sibling replacing
// a field the model already declares.
func TestMergeEditFieldsDoesNotOverwrite(t *testing.T) {
	own := &provider.EndpointSchema{
		Fields: []provider.EndpointField{field("image_urls", false, 2)},
	}
	edit := &provider.EndpointSchema{
		Fields: []provider.EndpointField{field("image_urls", true, 16)},
	}

	mergeEditFields(own, edit)

	if f, _ := own.Field("image_urls"); f.MaxItems == nil || *f.MaxItems != 2 {
		t.Fatalf("the model's own declaration must win: %+v", f)
	}
	if len(own.Fields) != 1 {
		t.Fatalf("duplicate field appended: %+v", own.Fields)
	}
}

// TestUpstreamEditFromOptions covers the option the fal adapter uses to swap
// endpoints, including a malformed value.
func TestUpstreamEditFromOptions(t *testing.T) {
	got := upstreamEditFromOptions([]byte(`{"upstream_edit":"krea/v3/edit"}`))
	if got != "krea/v3/edit" {
		t.Fatalf("got %q", got)
	}
	if v := upstreamEditFromOptions(nil); v != "" {
		t.Fatalf("nil opts: %q", v)
	}
	if v := upstreamEditFromOptions([]byte("{oops")); v != "" {
		t.Fatalf("broken json should be ignored: %q", v)
	}
}

func enumField(name string, values ...string) provider.EndpointField {
	return provider.EndpointField{Name: name, Enum: values}
}

// TestMergeEditFieldsNarrowsEnums is the regression guard for a real bug: a
// text-to-image model advertising a value only its own endpoint accepts. The
// user could pick svg AND attach an image, at which point the adapter repoints
// the request at the edit endpoint, which does not accept svg — a guaranteed
// failure the declaration itself invited.
func TestMergeEditFieldsNarrowsEnums(t *testing.T) {
	own := &provider.EndpointSchema{Fields: []provider.EndpointField{
		field("prompt", true, 0),
		enumField("output_format", "jpeg", "png", "webp", "gif", "svg"),
		enumField("image_size", "square_hd", "auto"),
	}}
	edit := &provider.EndpointSchema{Fields: []provider.EndpointField{
		enumField("output_format", "jpeg", "png", "webp", "gif"),
		enumField("image_size", "square_hd", "auto"),
		field("image_urls", true, 16),
	}}

	mergeEditFields(own, edit)

	f, _ := own.Field("output_format")
	if len(f.Enum) != 4 {
		t.Fatalf("output_format should narrow to the intersection: %v", f.Enum)
	}
	for _, v := range f.Enum {
		if v == "svg" {
			t.Fatalf("svg leaked into a model that can edit: %v", f.Enum)
		}
	}
	// Identical enums are unchanged.
	if g, _ := own.Field("image_size"); len(g.Enum) != 2 {
		t.Fatalf("image_size should be untouched: %v", g.Enum)
	}
}

// TestMergeEditFieldsKeepsOrder makes sure narrowing preserves the model's own
// option order, so the UI does not reshuffle when a model is edited.
func TestMergeEditFieldsKeepsOrder(t *testing.T) {
	own := &provider.EndpointSchema{Fields: []provider.EndpointField{
		enumField("f", "b", "a", "c"),
	}}
	edit := &provider.EndpointSchema{Fields: []provider.EndpointField{
		enumField("f", "c", "a"),
	}}
	mergeEditFields(own, edit)
	f, _ := own.Field("f")
	want := []string{"a", "c"}
	// own order is b,a,c → intersection is a,c.
	if len(f.Enum) != len(want) || f.Enum[0] != "a" || f.Enum[1] != "c" {
		t.Fatalf("got %v want %v", f.Enum, want)
	}
}

// TestMergeEditFieldsUnionEmpty guards the case where narrowing would erase a
// field: an empty intersection must not blank the list, or the control would
// silently disappear.
func TestMergeEditFieldsUnionEmpty(t *testing.T) {
	own := &provider.EndpointSchema{Fields: []provider.EndpointField{
		enumField("f", "x", "y"),
	}}
	edit := &provider.EndpointSchema{Fields: []provider.EndpointField{
		enumField("f", "z"),
	}}
	mergeEditFields(own, edit)
	if f, _ := own.Field("f"); len(f.Enum) != 2 {
		t.Fatalf("disjoint enums should stay as-is: %v", f.Enum)
	}
}

// TestCloneSchemaIsolatesCache guards the shared-cache mutation: imageSchema
// merges into what loadSchema returns, so the cache must hand out copies or a
// model sharing the endpoint inherits a sibling's image_urls.
func TestCloneSchemaIsolatesCache(t *testing.T) {
	cached := &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "output_format", Enum: []string{"png", "svg"}},
	}}
	got := cloneSchema(cached)
	mergeEditFields(got, &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "output_format", Enum: []string{"png"}},
		field("image_urls", true, 4),
	}})
	if len(cached.Fields) != 1 {
		t.Fatalf("merge leaked into the cached schema: %+v", cached.Fields)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("copy should carry the merge: %+v", got.Fields)
	}
	if cloneSchema(nil) != nil {
		t.Fatal("nil stays nil")
	}
}

// Enum narrowing assigns a new slice to the field, so a shallow Fields copy
// must be enough to keep the cached enum intact.
func TestCloneSchemaKeepsCachedEnum(t *testing.T) {
	cached := &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "output_format", Enum: []string{"png", "svg"}},
	}}
	got := cloneSchema(cached)
	mergeEditFields(got, &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "output_format", Enum: []string{"png"}},
	}})
	if len(cached.Fields[0].Enum) != 2 || len(got.Fields[0].Enum) != 1 {
		t.Fatalf("cached %v, copy %v", cached.Fields[0].Enum, got.Fields[0].Enum)
	}
}

// TestSchemaFailureExpiresSooner: a failed lookup must not hide capabilities
// for the full success TTL.
func TestSchemaFailureExpiresSooner(t *testing.T) {
	now := time.Now()
	fetched := now.Add(-(schemaFailTTL + time.Second))
	if (schemaCacheEntry{fetched: fetched, failed: true}).fresh(now) {
		t.Fatal("failed entry should have expired")
	}
	if !(schemaCacheEntry{fetched: fetched}).fresh(now) {
		t.Fatal("successful entry should still be fresh")
	}
}

// TestMergeVideoFieldsAddsAllMissingFields pins the fix that made h3-max's
// aspect_ratio reachable: the reference sibling carries fields the primary
// endpoint lacks (MiniMax i2v has no aspect_ratio, its r2v does), and an earlier
// version only merged the reference_* family, hiding a usable control.
func TestMergeVideoFieldsAddsAllMissingFields(t *testing.T) {
	own := &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "prompt", Required: true},
		{Name: "image_url"},
		{Name: "duration", Default: float64(5)},
	}}
	ref := &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "prompt", Required: true},
		{Name: "aspect_ratio", Enum: []string{"adaptive", "16:9", "9:16"}, Default: "adaptive"},
		{Name: "reference_image_urls", Required: true},
		{Name: "reference_video_urls"},
	}}

	mergeVideoFields(own, ref)

	for _, name := range []string{"aspect_ratio", "reference_image_urls", "reference_video_urls"} {
		f, ok := own.Field(name)
		if !ok {
			t.Fatalf("%s should be merged in: %+v", name, own.Fields)
		}
		if f.Required {
			t.Fatalf("%s is only reachable in reference mode, must not be required: %+v", name, f)
		}
	}
	// The model's own fields stay as they were.
	if f, _ := own.Field("prompt"); !f.Required {
		t.Fatalf("prompt must stay required: %+v", f)
	}
	// No duplicates.
	seen := map[string]int{}
	for _, f := range own.Fields {
		seen[f.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Fatalf("%s appears %d times: %+v", name, n, own.Fields)
		}
	}
}

// TestMergeVideoFieldsNarrowsEnum keeps a value from being offered that only one
// of the two endpoints accepts.
func TestMergeVideoFieldsNarrowsEnum(t *testing.T) {
	own := &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "resolution", Enum: []string{"480P", "768P", "1080P", "4K"}},
	}}
	ref := &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "resolution", Enum: []string{"480P", "768P", "1080P"}},
	}}

	mergeVideoFields(own, ref)

	f, _ := own.Field("resolution")
	if len(f.Enum) != 3 {
		t.Fatalf("resolution should narrow to the intersection: %v", f.Enum)
	}
	for _, v := range f.Enum {
		if v == "4K" {
			t.Fatalf("4K leaked into a model that cannot use it: %v", f.Enum)
		}
	}

	// A disjoint enum keeps the model's own values rather than vanishing.
	own2 := &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "resolution", Enum: []string{"A"}},
	}}
	ref2 := &provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "resolution", Enum: []string{"B"}},
	}}
	mergeVideoFields(own2, ref2)
	if f2, _ := own2.Field("resolution"); len(f2.Enum) != 1 || f2.Enum[0] != "A" {
		t.Fatalf("disjoint enums should keep the model's own: %v", f2.Enum)
	}
}
