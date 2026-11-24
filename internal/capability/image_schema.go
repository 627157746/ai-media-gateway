package capability

// Deriving image capabilities from a vendor's published endpoint schema. This
// replaces the hand-maintained per-model tables: fal adds and changes models
// constantly, and any list copied into Go silently rots.

import (
	"encoding/json"
	"strings"

	"ai-media-gateway/internal/provider"
)

// sizeFieldNames are the vendor field names that select output dimensions.
// fal uses image_size for a pixel/enum size and aspect_ratio for a ratio enum.
var sizeFieldNames = []string{"image_size", "aspect_ratio", "aspectRatio", "size"}

// ImageFromSchema builds an ImageSpec from an endpoint's declared inputs. A
// field the schema does not list is never advertised, so clients cannot send
// parameters the endpoint would reject.
func ImageFromSchema(s provider.EndpointSchema) ImageSpec {
	var spec ImageSpec

	spec.Defaults = map[string]string{}
	setDefault := func(key string, f provider.EndpointField) {
		if v, ok := f.Default.(string); ok && v != "" {
			spec.Defaults[key] = v
		}
	}

	// Size: the first documented size field wins, and its field name tells the
	// adapter how to forward it.
	for _, name := range sizeFieldNames {
		f, ok := s.Field(name)
		if !ok {
			continue
		}
		spec.Sizes = f.Enum
		spec.SizeMode = "image_size"
		if name == "aspect_ratio" || name == "aspectRatio" {
			spec.SizeMode = "aspect_ratio"
		}
		setDefault("size", f)
		break
	}

	if f, ok := s.Field("num_images"); ok && f.Maximum != nil {
		spec.MaxN = *f.Maximum
	}

	if f, ok := s.Field("quality"); ok {
		spec.Qualities = f.Enum
		setDefault("quality", f)
	}
	if f, ok := s.Field("background"); ok {
		spec.Backgrounds = f.Enum
		setDefault("background", f)
	}
	if f, ok := s.Field("output_format"); ok {
		spec.Formats = f.Enum
		setDefault("format", f)
	}
	if f, ok := s.Field("resolution"); ok {
		spec.Resolutions = f.Enum
		setDefault("resolution", f)
	}
	if len(spec.Defaults) == 0 {
		spec.Defaults = nil
	}

	// Reference images: image_urls presence means the endpoint can edit, and
	// required means it can ONLY edit.
	if f, ok := s.Field("image_urls"); ok {
		spec.SupportsEdit = true
		spec.RequiresImages = f.Required
		spec.MaxEditImages = 0
		if f.MaxItems != nil {
			spec.MaxEditImages = *f.MaxItems
		}
	}
	if _, ok := s.Field("mask_url"); ok {
		spec.SupportsMask = true
	}

	return spec
}

// ImageFromSchemaForKinds is the entry point used by the models endpoint: it
// derives from a schema when available and falls back to the hand-declared
// table when the vendor publishes none.
func ImageFromSchemaForKinds(s *provider.EndpointSchema, providerType, upstream string, opts []byte) ImageSpec {
	if s != nil && len(s.Fields) > 0 {
		spec := ImageFromSchema(*s)
		return applyImageOptions(spec, opts)
	}
	return ImageFromOptions(providerType, upstream, opts)
}

// applyImageOptions layers a model's own options over a derived spec.
func applyImageOptions(spec ImageSpec, opts []byte) ImageSpec {
	if len(opts) == 0 {
		return spec
	}
	var o falOptKeys
	if json.Unmarshal(opts, &o) != nil {
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

// IsEditEndpoint reports whether an upstream path targets image-to-image.
func IsEditEndpoint(upstream string) bool {
	return strings.HasSuffix(strings.TrimSuffix(strings.TrimSpace(upstream), "/"), "/edit")
}
