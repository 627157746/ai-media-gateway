package capability

import (
	"encoding/json"
	"strings"
)

// ImageSpec describes what an image model accepts. Fields a model cannot take
// are omitted from the declaration, so clients stop rendering inputs that would
// be silently dropped upstream.
//
// Capability belongs to the *endpoint*, not the platform: fal's
// gpt-image-2.6/sunburst/text-to-image and its /edit sibling take different
// parameters, so the same provider type yields different specs per model.
type ImageSpec struct {
	// Sizes lists accepted `size` values. For models whose `image_size` is an
	// enum (fal's gpt-image-2), these are the enum names, not pixel pairs.
	Sizes []string `json:"sizes,omitempty"`
	// SizeMode says how size reaches the vendor: "image_size" (pixel WxH or
	// enum) or "aspect_ratio" (derived ratio). Mirrors fal's model options.
	SizeMode string `json:"size_mode,omitempty"`
	// MaxN is the ceiling on `n`. This is the vendor's documented maximum.
	MaxN int `json:"max_n,omitempty"`
	// Qualities lists accepted `quality` values ("" when unsupported).
	Qualities []string `json:"qualities,omitempty"`
	// Resolutions lists accepted `resolution` values. Empty for every provider
	// declared here: none of the fetched schemas documented such a field, and
	// the fal adapter only forwards `resolution` when a model maps it.
	Resolutions []string `json:"resolutions,omitempty"`
	// Backgrounds lists accepted `background` values ("" when unsupported).
	Backgrounds []string `json:"backgrounds,omitempty"`
	// Formats lists accepted `response_format` / `output_format` values.
	Formats []string `json:"formats,omitempty"`
	// SupportsEdit means reference images are accepted (image-to-image).
	SupportsEdit bool `json:"supports_edit"`
	// RequiresImages means reference images are mandatory: the endpoint rejects
	// a request without them. Edit-only endpoints behave this way.
	RequiresImages bool `json:"requires_images,omitempty"`
	// SupportsMask means an inpainting mask is accepted.
	SupportsMask bool `json:"supports_mask"`
	// MaxEditImages caps reference images; 0 with SupportsEdit means unbounded.
	MaxEditImages int `json:"max_edit_images,omitempty"`
	// FirstImageIsSource means image 1 is the picture being edited and only the
	// remaining images are references. Muse behaves this way (it has no separate
	// edit endpoint, a source image is what makes the job an edit); fal's edit
	// endpoints treat every image as a reference instead.
	FirstImageIsSource bool `json:"first_image_is_source,omitempty"`

	// Defaults records the vendor's own default for each input, keyed by the
	// logical field name (size, quality, background, format). Without it a
	// client cannot tell which option applies when the user leaves a control
	// alone, and "默认" alone does not say what the default is.
	Defaults map[string]string `json:"defaults,omitempty"`
}

// imageDefaults is the shape given to a provider we have no schema for. It is
// deliberately minimal: offering an option we cannot verify is worse than
// offering none, because the user only discovers the problem after paying.
var imageDefaults = ImageSpec{
	Sizes:    []string{"auto", "1024x1024"},
	SizeMode: "image_size",
	MaxN:     1,
	Formats:  []string{"png"},
}

// gptImage26T2I is the verified schema for fal's
// openai/gpt-image-2.6/{sunburst,flare}/text-to-image (category
// text-to-image):
//
//	prompt:         required
//	image_size:     square_hd | square | portrait_4_3 | portrait_16_9 |
//	                landscape_4_3 | landscape_16_9 | auto, or {width,height}
//	                (max 14142 per side), default landscape_4_3
//	quality:        auto | low | medium | high | xhigh | max, default high
//	background:     auto | transparent | opaque
//	output_format:  jpeg | png | webp | gif | svg, default png
//	num_images:     1..10, default 1
//	reasoning_effort: low | medium | high (not surfaced by the gateway yet)
//
// There is no image_urls / mask_url field, so text-to-image cannot edit.
var gptImage26T2I = ImageSpec{
	Sizes: []string{
		"auto", "square_hd", "square",
		"portrait_4_3", "portrait_16_9",
		"landscape_4_3", "landscape_16_9",
	},
	SizeMode:    "image_size",
	MaxN:        10,
	Qualities:   []string{"auto", "low", "medium", "high", "xhigh", "max"},
	Backgrounds: []string{"auto", "transparent", "opaque"},
	Formats:     []string{"jpeg", "png", "webp", "gif", "svg"},
	Defaults: map[string]string{
		"size": "landscape_4_3", "quality": "high",
		"background": "auto", "format": "png",
	},
}

// gptImage26Edit is the verified schema for fal's
// openai/gpt-image-2.6/{sunburst,flare}/edit (category image-to-image). It
// shares quality/background/num_images with the t2i sibling, but:
//
//	required:       prompt AND image_urls (max 16)
//	image_size:     same enum, default auto (inferred from the input images)
//	output_format:  jpeg | png | webp | gif (no svg)
//	mask_url:       supported
var gptImage26Edit = ImageSpec{
	Sizes: []string{
		"auto", "square_hd", "square",
		"portrait_4_3", "portrait_16_9",
		"landscape_4_3", "landscape_16_9",
	},
	SizeMode:       "image_size",
	MaxN:           10,
	Qualities:      []string{"auto", "low", "medium", "high", "xhigh", "max"},
	Backgrounds:    []string{"auto", "transparent", "opaque"},
	Formats:        []string{"jpeg", "png", "webp", "gif"},
	SupportsEdit:   true,
	RequiresImages: true,
	SupportsMask:   true,
	MaxEditImages:  16,
	Defaults: map[string]string{
		"size": "auto", "quality": "high",
		"background": "auto", "format": "png",
	},
}

// imageOverrides records per-provider schemas. A provider only appears here
// once its parameter list has been read from documentation, never guessed.
// The fal entry is resolved per model by imageForFal.
var imageOverrides = map[string]ImageSpec{
	// Muse's media bridge takes one endpoint for both directions, so one
	// declaration covers text-to-image and image-to-image. Verified against the
	// Pi image plugin's muse adapter: single image per job, up to 6 reference
	// images, optional format (png/jpg/jpeg/webp), no size/quality/background
	// parameters at all, and the /result payload may be WebP regardless of what
	// was requested.
	"muse": {
		MaxN:               1,
		Formats:            []string{"png", "jpg", "jpeg", "webp"},
		SupportsEdit:       true,
		MaxEditImages:      6,
		FirstImageIsSource: true,
		// Docs: format defaults to webp when omitted.
		Defaults: map[string]string{"format": "webp"},
	},
	"openai": {
		// OpenAI's own images API. NOTE: not yet re-verified against the
		// current docs in this pass; treated as the conservative baseline.
		Sizes:         []string{"auto", "1024x1024", "1024x1536", "1536x1024"},
		SizeMode:      "image_size",
		MaxN:          10,
		Qualities:     []string{"auto", "low", "medium", "high"},
		Backgrounds:   []string{"auto", "opaque", "transparent"},
		Formats:       []string{"png", "jpeg", "webp"},
		SupportsEdit:  true,
		SupportsMask:  true,
		MaxEditImages: 16,
	},
}

// falOptKeys are the model options that change an image model's capabilities.
// Kept here so the adapter and the declaration read the same JSON.
type falOptKeys struct {
	UpstreamEdit   string   `json:"upstream_edit"`
	SupportsEdit   *bool    `json:"supports_edit"`
	SupportsMask   *bool    `json:"supports_mask"`
	MaxEditImages  *int     `json:"max_edit_images"`
	RequiresImages *bool    `json:"requires_images"`
	Sizes          []string `json:"sizes"`
	SizeMode       *string  `json:"size_mode"`
	MaxN           *int     `json:"max_n"`
	Qualities      []string `json:"qualities"`
	Resolutions    []string `json:"resolutions"`
	Backgrounds    []string `json:"backgrounds"`
	Formats        []string `json:"formats"`
}

// isEditEndpoint reports whether a fal endpoint URL targets image-to-image.
// fal's own catalog reports these as category "image-to-image" and their paths
// end in /edit, which is what the gateway stores as the model's upstream.
func isEditEndpoint(upstream string) bool {
	return strings.HasSuffix(strings.TrimSuffix(strings.TrimSpace(upstream), "/"), "/edit")
}

// isGptImage26 reports whether an upstream is one of the OpenAI gpt-image-2.6
// endpoints whose schema is transcribed below. Any other fal model must not
// inherit these parameters: fal hosts hundreds of models with entirely
// different inputs, so applying the gpt-image list to them would advertise
// options the endpoint rejects.
func isGptImage26(upstream string) bool {
	return strings.HasPrefix(strings.TrimSpace(upstream), "openai/gpt-image-2")
}

// imageForFal picks the endpoint schema for one fal model. Only the endpoints
// whose schema was actually read get a specific declaration; everything else
// falls back to the conservative minimum until its own schema is available.
func imageForFal(upstream string, o falOptKeys) ImageSpec {
	if !isGptImage26(upstream) {
		// Unknown fal model: declare only what is safe. Editing is still
		// advertised when the model names an edit endpoint, because that is
		// the model's own configuration, not an assumption about its inputs.
		spec := imageDefaults
		if o.UpstreamEdit != "" || isEditEndpoint(upstream) {
			spec.SupportsEdit = true
		}
		return spec
	}
	if isEditEndpoint(upstream) {
		return gptImage26Edit
	}
	spec := gptImage26T2I
	if o.UpstreamEdit != "" {
		// Editing swaps to the /edit endpoint, which also accepts a mask.
		spec.SupportsEdit = true
		spec.SupportsMask = true
		spec.MaxEditImages = 16
	}
	return spec
}

// ImageFor returns the declared capabilities for a provider type, ignoring
// per-model options. Use ImageFromOptions when the model is known. For fal this
// is the conservative minimum, because the provider hosts models with
// incompatible inputs and only a per-endpoint schema can describe one.
func ImageFor(providerType string) ImageSpec {
	if s, ok := imageOverrides[providerType]; ok {
		return s
	}
	return imageDefaults
}

// ImageFromOptions returns the capabilities of one model: the endpoint's own
// schema (upstream decides which fal schema applies) with the model's options
// layered on top.
func ImageFromOptions(providerType, upstream string, opts []byte) ImageSpec {
	var o falOptKeys
	parsed := false
	if len(opts) > 0 {
		parsed = json.Unmarshal(opts, &o) == nil
	}

	var spec ImageSpec
	if providerType == "fal" {
		spec = imageForFal(upstream, o)
	} else {
		spec = ImageFor(providerType)
	}
	if !parsed {
		return spec
	}

	if len(o.Sizes) > 0 {
		spec.Sizes = o.Sizes
	}
	if o.SizeMode != nil {
		spec.SizeMode = *o.SizeMode
	}
	if o.MaxN != nil {
		spec.MaxN = *o.MaxN
	}
	if len(o.Qualities) > 0 {
		spec.Qualities = o.Qualities
	}
	if len(o.Resolutions) > 0 {
		spec.Resolutions = o.Resolutions
	}
	if len(o.Backgrounds) > 0 {
		spec.Backgrounds = o.Backgrounds
	}
	if len(o.Formats) > 0 {
		spec.Formats = o.Formats
	}
	if o.SupportsEdit != nil {
		spec.SupportsEdit = *o.SupportsEdit
	}
	if o.RequiresImages != nil {
		spec.RequiresImages = *o.RequiresImages
	}
	if o.SupportsMask != nil {
		spec.SupportsMask = *o.SupportsMask
	}
	if o.MaxEditImages != nil {
		spec.MaxEditImages = *o.MaxEditImages
	}
	return spec
}

// ContainsFold reports whether list contains v, case-insensitively.
func ContainsFold(list []string, v string) bool {
	for _, item := range list {
		if strings.EqualFold(item, v) {
			return true
		}
	}
	return false
}
