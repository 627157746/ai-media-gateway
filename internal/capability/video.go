package capability

// Video capability declarations, derived from the vendor's published endpoint
// schema rather than a hand-maintained table. fal publishes one OpenAPI document
// per endpoint, so the same code describes any new video model.
//
// Two modes exist and a vendor usually exposes them as separate endpoints:
//
//   - image-to-video ("frames"): animate a supplied first frame, optional last
//     frame. The prompt describes the motion.
//   - reference-to-video ("reference"): the supplied images condition identity,
//     style or scene rather than fixing the first frame; some vendors also take
//     reference videos and audio.
//
// Verified against https://fal.ai/api/openapi/queue/openapi.json?endpoint_id=...

import (
	"encoding/json"
	"strconv"
	"strings"

	"ai-media-gateway/internal/provider"
)

// VideoModes are the request modes the gateway distinguishes.
const (
	VideoModeFrames    = "frames"
	VideoModeReference = "reference"
)

// VideoSpec describes what a video model accepts.
type VideoSpec struct {
	// Modes lists the supported request modes (frames, reference).
	Modes []string `json:"modes,omitempty"`
	// Durations lists accepted clip lengths in seconds when the vendor
	// constrains them to a fixed set; empty means any positive value.
	Durations []string `json:"durations,omitempty"`
	// DurationDefault is the vendor's own default length, in seconds.
	DurationDefault string `json:"duration_default,omitempty"`
	// DurationMin / DurationMax bound the length when the vendor publishes a
	// range instead of an enum (Alibaba allows 2-30 seconds, for example).
	DurationMin int `json:"duration_min,omitempty"`
	DurationMax int `json:"duration_max,omitempty"`
	// Ratios lists accepted aspect ratios.
	Ratios []string `json:"ratios,omitempty"`
	// RatioDefault is the vendor's default ratio.
	RatioDefault string `json:"ratio_default,omitempty"`
	// Resolutions lists accepted resolution labels (vendor casing preserved).
	Resolutions []string `json:"resolutions,omitempty"`
	// ResolutionDefault is the vendor's default resolution.
	ResolutionDefault string `json:"resolution_default,omitempty"`
	// SupportsFirstFrame means a first-frame image is accepted.
	SupportsFirstFrame bool `json:"supports_first_frame"`
	// RequiresImages means the model cannot run without at least one image
	// (nothing is generated from the prompt alone).
	RequiresImages bool `json:"requires_images,omitempty"`
	// SupportsLastFrame means a last-frame image is accepted.
	SupportsLastFrame bool `json:"supports_last_frame"`
	// MaxReferences caps reference images; 0 means the mode is unsupported.
	MaxReferences int `json:"max_references,omitempty"`
	// MaxReferenceVideos caps reference videos; 0 means unsupported.
	MaxReferenceVideos int `json:"max_reference_videos,omitempty"`
	// MaxReferenceAudios caps reference audio clips; 0 means unsupported.
	MaxReferenceAudios int `json:"max_reference_audios,omitempty"`
	// SupportsAudio means the generated clip can carry audio.
	SupportsAudio bool `json:"supports_audio"`
	// SupportsNegativePrompt means a negative prompt field exists.
	SupportsNegativePrompt bool `json:"supports_negative_prompt,omitempty"`
	// ValueLabels gives the Chinese display text for declared enum values,
	// keyed by logical field (ratio, resolution). Filled by WithLabels when
	// serving /v1/models; unlabelled values are absent.
	ValueLabels map[string]map[string]string `json:"value_labels,omitempty"`
}

// videoDefaults is what an endpoint with no readable schema can promise. It is
// deliberately almost empty: a client must never be offered an input the vendor
// would reject.
var videoDefaults = VideoSpec{}

// museVideo is Muse's bridge. ONE endpoint covers text-to-video and
// image-to-video, and the vendor fixes the output at roughly 10 seconds of mp4.
//
// It has no reference-to-video mode: reference_images_base64 is an ADDITIONAL
// hint (up to 6 images, referenced from the prompt as 第一张参考图…) that rides
// along with either a plain prompt or a source image — it never drives the job on
// its own. Declaring a separate "reference" mode would offer a choice the vendor
// does not have.
//
// No duration, ratio, resolution or format control exists, so none is declared.
var museVideo = VideoSpec{
	Modes: []string{VideoModeFrames},
	// The first frame is optional: without one Muse renders from the prompt alone.
	SupportsFirstFrame: true,
	// Reference images share the image endpoint's cap of 6.
	MaxReferences: 6,
}

// videoModeFromPath reads the mode straight from the endpoint suffix. fal names
// these consistently, so the suffix IS the contract:
//
//	alibaba/wan-3.0/image-to-video        -> frames
//	alibaba/wan-3.0/reference-to-video    -> reference
//	alibaba/wan-3.0/text-to-video         -> (needs no image)
//
// Suffix order matters: reference-to-video must be checked before
// image-to-video because both end in "-to-video".
func videoModeFromPath(upstream string) string {
	u := strings.ToLower(strings.TrimSpace(upstream))
	u = strings.TrimSuffix(u, "/")
	switch {
	case strings.HasSuffix(u, "reference-to-video"), strings.HasSuffix(u, "/reference"):
		return VideoModeReference
	case strings.HasSuffix(u, "image-to-video"), strings.HasSuffix(u, "/image"):
		return VideoModeFrames
	case strings.HasSuffix(u, "text-to-video"), strings.HasSuffix(u, "/text"):
		return "" // prompt only
	}
	return ""
}

// clipsInClipsOut reports endpoints whose suffix says they transform an existing
// video (extend/reframe/3d/...). The gateway has no field for an input clip, so
// these cannot be driven.
func clipsInClipsOut(upstream string) bool {
	u := strings.ToLower(strings.TrimSpace(upstream))
	for _, suffix := range []string{"extend-video", "reframe-video", "video-to-video", "recast", "relight", "insert-video", "3d-to-video"} {
		if strings.HasSuffix(u, suffix) {
			return true
		}
	}
	return false
}

// VideoFromOptions returns a video model's capabilities from the endpoint suffix
// alone, for callers that cannot fetch the schema.
func VideoFromOptions(providerType, upstream string, opts []byte) VideoSpec {
	if clipsInClipsOut(upstream) {
		return VideoSpec{}
	}
	var spec VideoSpec
	if providerType == "muse" {
		spec = museVideo
	} else {
		spec = videoDefaults
		if m := videoModeFromPath(upstream); m != "" {
			spec.Modes = []string{m}
		}
	}
	return applyVideoOptions(spec, opts)
}

// applyVideoOptions layers a model's own options over the derived spec.
//
// requires_images is the one flag that cannot be inferred reliably: MiniMax
// declares image_url as OPTIONAL on its image-to-video endpoint even though that
// endpoint exists to animate a supplied frame. A deployment that only wants
// image-driven clips says so here instead of trusting the vendor's optionality.
func applyVideoOptions(spec VideoSpec, opts []byte) VideoSpec {
	if len(opts) == 0 {
		return spec
	}
	var o struct {
		RequiresImages *bool    `json:"requires_images"`
		Modes          []string `json:"modes"`
	}
	if json.Unmarshal(opts, &o) != nil {
		return spec
	}
	if o.RequiresImages != nil {
		spec.RequiresImages = *o.RequiresImages
	}
	if len(o.Modes) > 0 {
		spec.Modes = o.Modes
	}
	return spec
}

// fieldMatches reports whether a field means one of the given concepts, judged
// by BOTH its name and the vendor's own description. Name-only matching is what
// broke Alibaba wan: it calls the first frame start_image_url while MiniMax
// calls it image_url, and a name list can never be complete. The description
// says "First frame of the generated video" in both cases.
func fieldMatches(f provider.EndpointField, names []string, phrases []string) bool {
	n := strings.ToLower(f.Name)
	for _, want := range names {
		if n == want {
			return true
		}
	}
	d := strings.ToLower(f.Description)
	for _, phrase := range phrases {
		if phrase != "" && strings.Contains(d, phrase) {
			return true
		}
	}
	return false
}

// findField returns the first field matching any name or description phrase.
func findField(s provider.EndpointSchema, names, phrases []string) (provider.EndpointField, bool) {
	for _, f := range s.Fields {
		if fieldMatches(f, names, phrases) {
			return f, true
		}
	}
	return provider.EndpointField{}, false
}

// VideoFromSchema describes one video endpoint. The MODE comes from the path
// suffix (fal names these consistently: .../image-to-video, .../reference-to-video,
// .../text-to-video); the schema then only decides which inputs that mode takes.
// The model's options are applied LAST so an explicit requires_images or modes
// declaration wins over anything inferred from the schema.
func VideoFromSchema(s provider.EndpointSchema, providerType, upstream string, opts []byte) VideoSpec {
	if clipsInClipsOut(upstream) {
		// The gateway has no field for an input clip.
		return VideoSpec{}
	}
	spec := VideoFromOptions(providerType, upstream, nil)
	if len(s.Fields) > 0 {
		spec = videoFromFields(spec, s)
	}
	return applyVideoOptions(spec, opts)
}

// videoFromFields fills a spec from the endpoint's declared inputs.
func videoFromFields(spec VideoSpec, s provider.EndpointSchema) VideoSpec {

	// First frame: the image that gets animated.
	first, hasFirst := findField(s,
		[]string{"image_url", "start_image_url", "first_frame_url", "first_frame", "image"},
		[]string{"first frame", "start image", "first_frame", "starting image", "input image"})
	if hasFirst {
		spec.SupportsFirstFrame = true
		// A REQUIRED first frame means the endpoint cannot run from the prompt
		// alone. Alibaba wan requires start_image_url; MiniMax's reference
		// endpoint does not.
		spec.RequiresImages = first.Required
	}

	// Last frame: the image the clip ends on.
	if _, ok := findField(s,
		[]string{"end_image_url", "last_frame_url", "last_frame", "tail_image_url"},
		[]string{"last frame", "end image", "last_frame", "ending image"}); ok {
		spec.SupportsLastFrame = true
	}

	// Reference images: separate from the first frame; they condition identity,
	// style or scene instead of fixing the opening shot.
	if f, ok := findField(s,
		[]string{"reference_image_urls", "reference_images", "reference_image_url"},
		[]string{"reference images", "reference image", "reference photo"}); ok {
		if !hasFirst || f.Name != first.Name {
			spec.MaxReferences = capOf(f)
			// A model whose path says image-to-video but which also exposes a
			// reference set genuinely serves both modes (MiniMax and Alibaba
			// both ship reference-to-video as a superset of image-to-video).
			if spec.Modes == nil {
				spec.Modes = []string{VideoModeReference}
			} else if !containsStr(spec.Modes, VideoModeReference) {
				spec.Modes = append(spec.Modes, VideoModeReference)
			}
		}
	}
	if f, ok := findField(s,
		[]string{"reference_video_urls", "reference_videos", "reference_video_url"},
		[]string{"reference videos", "reference video", "reference clip"}); ok {
		spec.MaxReferenceVideos = capOf(f)
	}
	if f, ok := findField(s,
		[]string{"reference_audio_urls", "reference_audios", "reference_audio_url"},
		[]string{"reference audio", "reference audios", "voice reference"}); ok {
		spec.MaxReferenceAudios = capOf(f)
	}

	// Duration: an enum with a default when the vendor fixes the options, or a
	// numeric range with a default when it allows any value in between.
	if f, ok := findField(s, []string{"duration", "seconds", "video_length"},
		[]string{"duration", "length in seconds", "clip length"}); ok {
		spec.Durations = f.Enum
		spec.DurationDefault = scalarString(f.Default)
		if f.Minimum != nil {
			spec.DurationMin = *f.Minimum
		}
		if f.Maximum != nil {
			spec.DurationMax = *f.Maximum
		}
	}
	if f, ok := findField(s, []string{"aspect_ratio", "ratio", "size"},
		[]string{"aspect ratio", "aspect_ratio"}); ok && len(f.Enum) > 0 {
		spec.Ratios = f.Enum
		spec.RatioDefault = scalarString(f.Default)
	}
	if f, ok := findField(s, []string{"resolution", "resolution_name", "video_size"},
		[]string{"resolution tier", "output resolution", "resolution"}); ok && len(f.Enum) > 0 {
		spec.Resolutions = f.Enum
		spec.ResolutionDefault = scalarString(f.Default)
	}
	if _, ok := findField(s, []string{"negative_prompt"},
		[]string{"negative prompt"}); ok {
		spec.SupportsNegativePrompt = true
	}

	// Audio: driving the output from a supplied track, or a boolean switch that
	// turns generated audio on. Both mean the clip can carry sound.
	if _, ok := findField(s, []string{"target_audio_url", "audio_url"},
		[]string{"audio to drive", "lip sync", "target audio"}); ok {
		spec.SupportsAudio = true
	}
	if f, ok := findField(s, []string{"audio"}, []string{"include generated audio", "generated audio"}); ok {
		if _, isBool := f.Default.(bool); isBool {
			spec.SupportsAudio = true
		}
	}
	return spec
}

// containsStr reports whether list holds v.
func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// capOf reports an array field's documented cap: the number when the vendor
// published maxItems, or -1 when the field exists but is unbounded. 0 would be
// ambiguous with "unsupported", so it is never returned here.
func capOf(f provider.EndpointField) int {
	if f.MaxItems != nil {
		return *f.MaxItems
	}
	return -1
}

// scalarString renders a schema default as a string, tolerating numbers because
// vendors mix "5" and 5 for the same duration field.
func scalarString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}
