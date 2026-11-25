package capability

// valueLabels maps a vendor enum value to the Chinese text a client shows for
// it, keyed by logical field. The request keeps sending the original value;
// only the display changes. This is the single copy: the admin playground and
// the canvas both read it from GET /v1/models (capabilities.value_labels)
// instead of keeping tables of their own.
var valueLabels = map[string]map[string]string{
	// fal's image_size presets describe a SHAPE, so the label is the aspect
	// ratio itself. The two square presets both mean 1:1 and differ only in
	// resolution, so they get a 大/小 hint; no pixel count is shown because the
	// vendor documents the ratio but not the size per preset.
	"size": {
		"auto":           "自动",
		"square_hd":      "1:1（大）",
		"square":         "1:1（小）",
		"portrait_4_3":   "3:4",
		"portrait_16_9":  "9:16",
		"landscape_4_3":  "4:3",
		"landscape_16_9": "16:9",
		// Some vendors encode the resolution TIER inside image_size instead of
		// exposing a separate resolution field, so 1K/1.5K/2K appear here.
		"auto_1K":   "自动 · 1K",
		"auto_1.5K": "自动 · 1.5K",
		"auto_2K":   "自动 · 2K",
		"auto_4K":   "自动 · 4K",
		"1024x1024": "1:1（1024×1024）",
		"1024x1536": "2:3（1024×1536）",
		"1536x1024": "3:2（1536×1024）",
	},
	"quality": {
		"auto": "自动", "low": "低", "medium": "中", "high": "高",
		"xhigh": "超高", "max": "最高",
	},
	"background": {"auto": "自动", "transparent": "透明", "opaque": "不透明"},
	"format": {
		"png": "PNG（无损）", "jpg": "JPG（有损）", "jpeg": "JPEG（有损）",
		"webp": "WebP（体积小）", "gif": "GIF（动图）", "svg": "SVG（矢量）",
	},
	"resolution": {
		"480P": "480P 流畅", "720P": "720P 高清", "768P": "768P 高清",
		"1080P": "1080P 全高清",
	},
	"ratio": {
		"auto": "自动", "adaptive": "自适应（跟随输入）",
	},
}

// labelsFor returns labels only for the values a model actually declares, so
// the payload never mentions options the model does not have. A value with no
// known label, or whose label equals the value itself ("16:9"), is left out;
// clients show such values as-is.
func labelsFor(lists map[string][]string) map[string]map[string]string {
	var out map[string]map[string]string
	for kind, values := range lists {
		known := valueLabels[kind]
		for _, v := range values {
			l, ok := known[v]
			if !ok || l == v {
				continue
			}
			if out == nil {
				out = map[string]map[string]string{}
			}
			if out[kind] == nil {
				out[kind] = map[string]string{}
			}
			out[kind][v] = l
		}
	}
	return out
}

// plus returns list with extra appended, without writing into list's backing
// array (the spec's own slices must not change).
func plus(list []string, extra ...string) []string {
	out := make([]string, 0, len(list)+len(extra))
	out = append(out, list...)
	for _, e := range extra {
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// WithLabels fills ValueLabels for the spec's declared values and defaults.
func (s ImageSpec) WithLabels() ImageSpec {
	d := s.Defaults
	s.ValueLabels = labelsFor(map[string][]string{
		"size":       plus(s.Sizes, d["size"]),
		"quality":    plus(s.Qualities, d["quality"]),
		"resolution": plus(s.Resolutions, d["resolution"]),
		"background": plus(s.Backgrounds, d["background"]),
		"format":     plus(s.Formats, d["format"]),
	})
	return s
}

// WithLabels fills ValueLabels for the spec's declared values and defaults.
func (s VideoSpec) WithLabels() VideoSpec {
	s.ValueLabels = labelsFor(map[string][]string{
		"ratio":      plus(s.Ratios, s.RatioDefault),
		"resolution": plus(s.Resolutions, s.ResolutionDefault),
	})
	return s
}
