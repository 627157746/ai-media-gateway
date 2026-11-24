package capability

import (
	"encoding/json"
	"testing"

	"ai-media-gateway/internal/provider"
)

func mustJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func vfield(name string, enum []string, def any, maxItems *int) provider.EndpointField {
	return provider.EndpointField{Name: name, Enum: enum, Default: def, MaxItems: maxItems}
}

func iptr(v int) *int { return &v }

// TestVideoFromSchemaImageToVideo pins the image-to-video surface: first frame,
// optional last frame, a fixed resolution enum with its default, and a numeric
// duration default. Reference media must NOT be advertised.
func TestVideoFromSchemaImageToVideo(t *testing.T) {
	s := provider.EndpointSchema{
		Endpoint: "minimax/h3-max/image-to-video",
		Fields: []provider.EndpointField{
			vfield("prompt", nil, nil, nil),
			vfield("prompt_expansion_mode", nil, "balanced", nil),
			vfield("image_url", nil, nil, nil),
			vfield("end_image_url", nil, nil, nil),
			vfield("duration", nil, float64(5), nil),
			vfield("resolution", []string{"480P", "768P", "1080P"}, "768P", nil),
			vfield("target_audio_url", nil, nil, nil),
		},
	}
	got := VideoFromSchema(s, "fal", "minimax/h3-max/image-to-video", nil)

	if len(got.Modes) != 1 || got.Modes[0] != VideoModeFrames {
		t.Fatalf("modes: %v", got.Modes)
	}
	if !got.SupportsFirstFrame || !got.SupportsLastFrame {
		t.Fatalf("frames: %+v", got)
	}
	if got.MaxReferences != 0 {
		t.Fatalf("image-to-video has no reference_image_urls: %d", got.MaxReferences)
	}
	if got.DurationDefault != "5" {
		t.Fatalf("numeric duration default should render as a string: %q", got.DurationDefault)
	}
	if got.ResolutionDefault != "768P" || len(got.Resolutions) != 3 {
		t.Fatalf("resolution: %+v", got)
	}
	if !got.SupportsAudio {
		t.Fatalf("target_audio_url implies audio support: %+v", got)
	}
	// aspect_ratio is absent from this endpoint, so no ratio list.
	if len(got.Ratios) != 0 || got.RatioDefault != "" {
		t.Fatalf("ratios must stay empty: %+v", got)
	}
}

// TestVideoFromSchemaReference pins the reference-to-video surface, which adds
// the reference_* family on top of the frame fields.
func TestVideoFromSchemaReference(t *testing.T) {
	s := provider.EndpointSchema{
		Endpoint: "minimax/h3-max/reference-to-video",
		Fields: []provider.EndpointField{
			vfield("prompt", nil, nil, nil),
			vfield("image_url", nil, nil, nil),
			vfield("end_image_url", nil, nil, nil),
			// fal documents maxItems on all three: 9 images, 3 videos, 3 audios.
			vfield("reference_image_urls", nil, nil, iptr(9)),
			vfield("reference_video_urls", nil, nil, iptr(3)),
			vfield("reference_audio_urls", nil, nil, iptr(3)),
			vfield("aspect_ratio", []string{"adaptive", "16:9", "9:16"}, "adaptive", nil),
			vfield("resolution", []string{"480P", "768P", "1080P"}, "768P", nil),
			vfield("duration", nil, float64(5), nil),
		},
	}
	got := VideoFromSchema(s, "fal", "minimax/h3-max/reference-to-video", nil)

	if len(got.Modes) != 1 || got.Modes[0] != VideoModeReference {
		t.Fatalf("the reference-to-video suffix means reference mode: %v", got.Modes)
	}
	if got.MaxReferences != 9 {
		t.Fatalf("max references should follow the documented maxItems: %d", got.MaxReferences)
	}
	if got.MaxReferenceVideos != 3 || got.MaxReferenceAudios != 3 {
		t.Fatalf("reference media caps: %+v", got)
	}
	if got.RatioDefault != "adaptive" || len(got.Ratios) != 3 {
		t.Fatalf("ratio: %+v", got)
	}
}

// TestVideoFromSchemaReferenceUnbounded reports -1 (supported, no published
// cap) so it is never confused with 0 (unsupported).
func TestVideoFromSchemaReferenceUnbounded(t *testing.T) {
	s := provider.EndpointSchema{Fields: []provider.EndpointField{
		vfield("reference_image_urls", nil, nil, nil),
	}}
	if got := VideoFromSchema(s, "fal", "acme/reference-to-video", nil); got.MaxReferences != -1 {
		t.Fatalf("unbounded should be -1, got %d", got.MaxReferences)
	}
}

// TestVideoFromSchemaReferenceWithCap honours a documented maxItems.
func TestVideoFromSchemaReferenceWithCap(t *testing.T) {
	s := provider.EndpointSchema{Fields: []provider.EndpointField{
		vfield("reference_image_urls", nil, nil, iptr(3)),
	}}
	got := VideoFromSchema(s, "fal", "acme/reference-to-video", nil)
	if got.MaxReferences != 3 {
		t.Fatalf("max references should follow maxItems: %d", got.MaxReferences)
	}
}

// TestVideoToVideoNotAdvertised guards endpoints the gateway cannot drive: they
// require an input clip, and VideoRequest has no field for one. The judgement
// comes from the endpoint suffix.
func TestVideoToVideoNotAdvertised(t *testing.T) {
	s := provider.EndpointSchema{Fields: []provider.EndpointField{
		vfield("prompt", nil, nil, nil),
		vfield("image_url", nil, nil, nil),
	}}
	for _, ep := range []string{
		"minimax/h3-max/extend-video",
		"minimax/h3-max/recast",
		"minimax/h3-max/reframe-video",
		"minimax/h3-max/3d-to-video",
		"fal-ai/kling/video-to-video",
	} {
		got := VideoFromSchema(s, "fal", ep, nil)
		if len(got.Modes) != 0 || got.SupportsFirstFrame || got.SupportsLastFrame {
			t.Fatalf("%s must declare nothing, got %+v", ep, got)
		}
	}
}

// TestLipSyncStaysUsable is the correction of a wrong assumption: the name
// minimax/h3-max/lip-sync/image-to-video contains "lip-sync", which an earlier
// blocklist matched, but its schema takes image_url + audio_url and needs no
// input clip at all. The suffix says image-to-video, so it is usable.
func TestLipSyncStaysUsable(t *testing.T) {
	s := provider.EndpointSchema{Fields: []provider.EndpointField{
		vfield("image_url", nil, nil, nil),
		vfield("audio_url", nil, nil, nil),
	}}
	got := VideoFromSchema(s, "fal", "minimax/h3-max/lip-sync/image-to-video", nil)
	if len(got.Modes) != 1 || got.Modes[0] != VideoModeFrames {
		t.Fatalf("lip-sync/image-to-video is frames mode: %+v", got)
	}
	if !got.SupportsFirstFrame {
		t.Fatalf("it takes image_url as the first frame: %+v", got)
	}
}

// TestVideoFromSchemaEmpty keeps an unreadable schema from looking capable.
func TestVideoFromSchemaEmpty(t *testing.T) {
	got := VideoFromSchema(provider.EndpointSchema{}, "fal", "unknown/model", nil)
	if len(got.Modes) != 0 || got.SupportsFirstFrame || got.SupportsLastFrame || got.MaxReferences != 0 {
		t.Fatalf("an empty schema must declare nothing: %+v", got)
	}
}

// TestVideoFromOptionsPathInference covers the schema-less fallback, which can
// only use the endpoint path.
func TestVideoFromOptionsPathInference(t *testing.T) {
	if got := VideoFromOptions("fal", "minimax/h3-max/image-to-video", nil); len(got.Modes) != 1 || got.Modes[0] != VideoModeFrames {
		t.Fatalf("image-to-video path: %+v", got)
	}
	if got := VideoFromOptions("fal", "minimax/h3-max/reference-to-video", nil); len(got.Modes) != 1 || got.Modes[0] != VideoModeReference {
		t.Fatalf("reference-to-video path: %+v", got)
	}
	// Path inference cannot prove frame support, so it must not claim it.
	if got := VideoFromOptions("fal", "minimax/h3-max/image-to-video", nil); got.SupportsFirstFrame {
		t.Fatalf("path alone cannot prove field support: %+v", got)
	}
}

// TestVideoSpecJSON keeps unset capability flags out of the response so a
// client does not render an input the vendor lacks.
func TestVideoSpecJSON(t *testing.T) {
	b, _ := mustJSON(VideoSpec{Modes: []string{VideoModeFrames}})
	for _, absent := range []string{"max_references", "reference_video", "durations", "ratios", "resolutions"} {
		if contains(b, absent) {
			t.Fatalf("%q should be omitted: %s", absent, b)
		}
	}
	if !contains(b, "supports_first_frame") {
		t.Fatalf("supports_first_frame must always be present: %s", b)
	}
}

// TestVideoFromSchemaByDescription is the regression guard for a real gap: the
// derivation first matched only image_url, so Alibaba wan (start_image_url) was
// reported as an image-to-video model that needs no image at all. The vendor's
// description carries the meaning the field name does not.
func TestVideoFromSchemaByDescription(t *testing.T) {
	// Shape taken verbatim from
	// https://fal.ai/api/openapi/queue/openapi.json?endpoint_id=alibaba/wan-3.0/image-to-video
	s := provider.EndpointSchema{
		Endpoint: "alibaba/wan-3.0/image-to-video",
		Fields: []provider.EndpointField{
			{Name: "start_image_url", Description: "First frame of the generated video.", Required: true},
			{Name: "end_image_url", Description: "Last frame of the generated video. Requires start_image_url."},
			{Name: "prompt", Description: "Text prompt describing the motion to generate."},
			{Name: "duration", Default: float64(5), Description: "Output duration in seconds."},
			{Name: "aspect_ratio", Enum: []string{"adaptive", "16:9", "9:16"}, Default: "adaptive"},
			{Name: "resolution", Enum: []string{"480p", "720p", "1080p"}, Default: "1080p"},
			{Name: "audio", Default: true, Description: "Include generated audio."},
		},
	}
	got := VideoFromSchema(s, "fal", "alibaba/wan-3.0/image-to-video", nil)

	if !got.SupportsFirstFrame {
		t.Fatalf("start_image_url IS the first frame: %+v", got)
	}
	if !got.SupportsLastFrame {
		t.Fatalf("end_image_url is the last frame: %+v", got)
	}
	if !got.SupportsAudio {
		t.Fatalf("a boolean audio field means the clip can carry audio: %+v", got)
	}
	if got.DurationDefault != "5" {
		t.Fatalf("duration default: %q", got.DurationDefault)
	}
	if got.ResolutionDefault != "1080p" {
		t.Fatalf("resolution default: %q", got.ResolutionDefault)
	}
	// Only one image in, so frames mode only; no reference set.
	if len(got.Modes) != 1 || got.Modes[0] != VideoModeFrames {
		t.Fatalf("modes: %v", got.Modes)
	}
	if got.MaxReferences != 0 {
		t.Fatalf("wan has no reference images: %d", got.MaxReferences)
	}
}

// TestVideoDurationRange pins a ranged duration (Alibaba: 2-30 seconds) which
// is a range, not an enum, so a client must not be offered 5/10 only.
func TestVideoDurationRange(t *testing.T) {
	lo, hi := 2, 30
	s := provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "start_image_url", Description: "First frame of the generated video."},
		{Name: "duration", Minimum: &lo, Maximum: &hi, Default: float64(5)},
	}}
	got := VideoFromSchema(s, "fal", "alibaba/wan-3.0/image-to-video", nil)
	if got.DurationMin != 2 || got.DurationMax != 30 {
		t.Fatalf("duration range: %+v", got)
	}
	if got.DurationDefault != "5" {
		t.Fatalf("duration default: %q", got.DurationDefault)
	}
	if len(got.Durations) != 0 {
		t.Fatalf("a range is not an enum: %v", got.Durations)
	}
}

// TestVideoFirstFrameNotConfusedWithReferences makes sure the image that gets
// animated is not also counted as a reference set.
func TestVideoFirstFrameNotConfusedWithReferences(t *testing.T) {
	s := provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "image_url", Description: "First frame of the generated video."},
	}}
	got := VideoFromSchema(s, "fal", "acme/image-to-video", nil)
	if len(got.Modes) != 1 || got.Modes[0] != VideoModeFrames {
		t.Fatalf("a first frame alone is frames mode: %v", got.Modes)
	}
	if got.MaxReferences != 0 {
		t.Fatalf("first frame must not become a reference: %d", got.MaxReferences)
	}
}

// TestMuseVideoHasNoReferenceMode pins the correction: Muse's
// reference_images_base64 is an ADDITIONAL hint that rides along with a prompt or
// a source image — it is not a reference-to-video mode. Declaring one offered a
// choice the vendor does not have.
func TestMuseVideoHasNoReferenceMode(t *testing.T) {
	got := VideoFromOptions("muse", "video", nil)
	if len(got.Modes) != 1 || got.Modes[0] != VideoModeFrames {
		t.Fatalf("muse has a single image-to-video mode: %v", got.Modes)
	}
	if !got.SupportsFirstFrame {
		t.Fatalf("a first frame is accepted: %+v", got)
	}
	if got.RequiresImages {
		t.Fatalf("Muse can render from the prompt alone: %+v", got)
	}
	if got.MaxReferences != 6 {
		t.Fatalf("reference images share the 6 cap: %d", got.MaxReferences)
	}
	// Muse has no reference videos or audio inputs at all.
	if got.MaxReferenceVideos != 0 || got.MaxReferenceAudios != 0 {
		t.Fatalf("muse takes no reference video/audio: %+v", got)
	}
	// And no duration/ratio/resolution controls.
	if got.DurationDefault != "" || got.DurationMin != 0 || got.DurationMax != 0 {
		t.Fatalf("muse fixes the duration: %+v", got)
	}
	if len(got.Ratios) != 0 || len(got.Resolutions) != 0 {
		t.Fatalf("muse has no ratio/resolution: %+v", got)
	}
}

// TestRequiresImagesFromRequiredField checks the derived requirement: a model
// whose first frame is REQUIRED cannot run from a prompt alone, while an optional
// one can.
func TestRequiresImagesFromRequiredField(t *testing.T) {
	required := provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "start_image_url", Required: true, Description: "First frame of the generated video."},
		{Name: "prompt"},
	}}
	if got := VideoFromSchema(required, "fal", "alibaba/wan-3.0/image-to-video", nil); !got.RequiresImages {
		t.Fatalf("a required start image must set requires_images: %+v", got)
	}

	optional := provider.EndpointSchema{Fields: []provider.EndpointField{
		{Name: "image_url", Description: "First frame of the generated video."},
		{Name: "prompt"},
	}}
	if got := VideoFromSchema(optional, "fal", "minimax/h3-max/image-to-video", nil); got.RequiresImages {
		t.Fatalf("an optional first frame must not set requires_images: %+v", got)
	}
}
