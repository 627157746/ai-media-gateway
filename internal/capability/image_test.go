package capability

import (
	"encoding/json"
	"testing"
)

// TestImageFalTextToImage pins the verified text-to-image schema:
// https://fal.ai/api/openapi/queue/openapi.json?endpoint_id=
//
//	openai/gpt-image-2.6/sunburst/text-to-image
func TestImageFalTextToImage(t *testing.T) {
	got := ImageFromOptions("fal", "openai/gpt-image-2.6/sunburst/text-to-image", nil)

	if got.SupportsEdit || got.RequiresImages || got.SupportsMask {
		t.Fatalf("text-to-image has no image_urls/mask_url field: %+v", got)
	}
	if got.MaxN != 10 {
		t.Fatalf("num_images max is 10, got %d", got.MaxN)
	}
	for _, q := range []string{"auto", "low", "medium", "high", "xhigh", "max"} {
		if !ContainsFold(got.Qualities, q) {
			t.Fatalf("quality %q missing from %v", q, got.Qualities)
		}
	}
	for _, f := range []string{"jpeg", "png", "webp", "gif", "svg"} {
		if !ContainsFold(got.Formats, f) {
			t.Fatalf("output_format %q missing from %v", f, got.Formats)
		}
	}
	if !ContainsFold(got.Sizes, "landscape_4_3") {
		t.Fatalf("sizes should be the image_size enum: %v", got.Sizes)
	}
	if len(got.Resolutions) != 0 {
		t.Fatalf("no resolution field is documented: %v", got.Resolutions)
	}
}

// TestImageFalEdit pins the verified edit schema, whose required fields are
// prompt AND image_urls (max 16) and whose output_format has no svg.
func TestImageFalEdit(t *testing.T) {
	got := ImageFromOptions("fal", "openai/gpt-image-2.6/flare/edit", nil)

	if !got.SupportsEdit || !got.RequiresImages || !got.SupportsMask {
		t.Fatalf("edit endpoint must require images: %+v", got)
	}
	if got.MaxEditImages != 16 {
		t.Fatalf("image_urls maxItems is 16, got %d", got.MaxEditImages)
	}
	if got.MaxN != 10 {
		t.Fatalf("num_images max is 10, got %d", got.MaxN)
	}
	if ContainsFold(got.Formats, "svg") {
		t.Fatalf("edit output_format has no svg: %v", got.Formats)
	}
	if !ContainsFold(got.Formats, "gif") {
		t.Fatalf("edit output_format includes gif: %v", got.Formats)
	}
}

// TestImageFalT2IWithUpstreamEdit is the live configuration: a text-to-image
// model that records an `upstream_edit`, so the adapter repoints the request
// when reference images are supplied. Edit becomes possible, but stays optional
// because the model itself is still a text-to-image endpoint.
func TestImageFalT2IWithUpstreamEdit(t *testing.T) {
	opts, _ := json.Marshal(map[string]any{
		"upstream_edit": "openai/gpt-image-2.6/sunburst/edit",
	})
	got := ImageFromOptions("fal", "openai/gpt-image-2.6/sunburst/text-to-image", opts)

	if !got.SupportsEdit || !got.SupportsMask {
		t.Fatalf("upstream_edit should enable editing: %+v", got)
	}
	if got.RequiresImages {
		t.Fatalf("text-to-image still works without images: %+v", got)
	}
	if got.MaxEditImages != 16 {
		t.Fatalf("edit endpoint allows 16 images, got %d", got.MaxEditImages)
	}
	// Still the t2i format list, because the endpoint only swaps on edit.
	if !ContainsFold(got.Formats, "svg") {
		t.Fatalf("t2i keeps svg until it swaps endpoints: %v", got.Formats)
	}
}

// TestImageFromOptionsOverride checks a model can narrow or widen the spec.
func TestImageFromOptionsOverride(t *testing.T) {
	opts, _ := json.Marshal(map[string]any{
		"sizes":           []string{"1024x1024"},
		"max_n":           2,
		"supports_mask":   false,
		"max_edit_images": 3,
	})
	got := ImageFromOptions("fal", "openai/gpt-image-2.6/sunburst/edit", opts)
	if len(got.Sizes) != 1 || got.Sizes[0] != "1024x1024" {
		t.Fatalf("sizes: %v", got.Sizes)
	}
	if got.MaxN != 2 || got.SupportsMask || got.MaxEditImages != 3 {
		t.Fatalf("overrides: %+v", got)
	}
	// Untouched fields keep the endpoint default.
	if len(got.Qualities) == 0 {
		t.Fatalf("qualities should survive override: %+v", got)
	}

	// Broken JSON must not zero the spec.
	bad := ImageFromOptions("fal", "openai/gpt-image-2.6/sunburst/edit", []byte("{oops"))
	if bad.MaxN != 10 || bad.MaxEditImages != 16 {
		t.Fatalf("bad json fallback: %+v", bad)
	}
}

// TestImageForKnownProviders pins the provider-level baseline, used only when
// the model is unknown. fal deliberately yields the conservative minimum: the
// provider hosts models with incompatible inputs, so a provider-wide gpt-image
// declaration would advertise parameters most endpoints reject.
func TestImageForKnownProviders(t *testing.T) {
	fal := ImageFor("fal")
	if fal.MaxN != imageDefaults.MaxN || func() bool { return len(fal.Sizes) == len(gptImage26T2I.Sizes) }() {
		t.Fatalf("fal baseline must stay the minimal default: %+v", fal)
	}
	if fal.SupportsEdit || fal.RequiresImages {
		t.Fatalf("fal baseline claims editing: %+v", fal)
	}
	// A known gpt-image endpoint still resolves to its verified schema.
	gpt := ImageFromOptions("fal", "openai/gpt-image-2.6/sunburst/text-to-image", nil)
	if gpt.MaxN != 10 || len(gpt.Qualities) != 6 {
		t.Fatalf("gpt-image endpoint should keep its schema: %+v", gpt)
	}

	oa := ImageFor("openai")
	if oa.MaxN != 10 || oa.MaxEditImages != 16 || !oa.SupportsEdit {
		t.Fatalf("openai: %+v", oa)
	}

	unk := ImageFor("brand-new")
	if unk.MaxN != imageDefaults.MaxN || len(unk.Sizes) == 0 {
		t.Fatalf("unknown fallback: %+v", unk)
	}
	if unk.SupportsEdit {
		t.Fatal("unknown provider must not claim edit support")
	}
}

// TestImageSpecJSON makes sure the wire shape stays stable: a nil list must be
// omitted rather than serialized as null, which clients would have to special
// case.
func TestImageSpecJSON(t *testing.T) {
	b, err := json.Marshal(ImageSpec{Sizes: []string{"1024x1024"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, absent := range []string{"qualities", "resolutions", "backgrounds", "requires_images"} {
		if contains(s, absent) {
			t.Fatalf("%q should be omitted from %s", absent, s)
		}
	}
	if !contains(s, "supports_edit") {
		t.Fatalf("supports_edit should always be present: %s", s)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

// TestMuseFirstImageIsSource pins the split that distinguishes Muse from fal:
// Muse has no separate edit endpoint, so supplying a source image is what turns
// a job into an edit and only the remaining images are references. Clients must
// be told, or the same files mean different things per provider.
func TestMuseFirstImageIsSource(t *testing.T) {
	muse := ImageFromOptions("muse", "muse", nil)
	if !muse.RequiresImages {
		// Muse can also do plain text-to-image, so images stay optional.
		if !muse.SupportsEdit || !muse.FirstImageIsSource {
			t.Fatalf("muse: %+v", muse)
		}
	}
	if muse.MaxEditImages != 6 {
		t.Fatalf("muse reference cap: %d", muse.MaxEditImages)
	}
	if muse.MaxN != 1 {
		t.Fatalf("muse produces one image per job: %d", muse.MaxN)
	}
	if ContainsFold(muse.Formats, "gif") || ContainsFold(muse.Formats, "svg") {
		t.Fatalf("muse formats: %v", muse.Formats)
	}

	// fal edit endpoints treat every image as a reference.
	fal := ImageFromOptions("fal", "openai/gpt-image-2.6/sunburst/edit", nil)
	if fal.FirstImageIsSource {
		t.Fatalf("fal edit should not claim a source image: %+v", fal)
	}
}

// TestFirstImageIsSourceJSON keeps the flag out of the wire format when unset,
// so existing clients see no change.
func TestFirstImageIsSourceJSON(t *testing.T) {
	b, err := json.Marshal(ImageSpec{SupportsEdit: true})
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(b), "first_image_is_source") {
		t.Fatalf("unset flag should be omitted: %s", b)
	}
	b2, err := json.Marshal(ImageSpec{SupportsEdit: true, FirstImageIsSource: true})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(b2), `"first_image_is_source":true`) {
		t.Fatalf("set flag should be present: %s", b2)
	}
}
