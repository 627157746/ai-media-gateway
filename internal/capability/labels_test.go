package capability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImageWithLabels(t *testing.T) {
	spec := ImageSpec{
		Sizes:     []string{"square_hd", "made_up"},
		Qualities: []string{"high"},
		Defaults:  map[string]string{"background": "transparent"},
	}
	got := spec.WithLabels().ValueLabels
	if got["size"]["square_hd"] != "1:1（大）" || got["quality"]["high"] != "高" {
		t.Fatalf("declared values must be labelled: %v", got)
	}
	if _, ok := got["size"]["made_up"]; ok {
		t.Fatalf("unknown value must be left out: %v", got)
	}
	if got["background"]["transparent"] != "透明" {
		t.Fatalf("a default must be labelled even when not listed: %v", got)
	}
	if _, ok := got["quality"]["low"]; ok {
		t.Fatalf("values the model does not declare must not appear: %v", got)
	}
	if spec.ValueLabels != nil {
		t.Fatal("WithLabels must not modify the receiver")
	}
}

func TestVideoWithLabels(t *testing.T) {
	got := VideoSpec{Ratios: []string{"16:9", "adaptive"}, Resolutions: []string{"720P"}}.WithLabels().ValueLabels
	if got["ratio"]["adaptive"] != "自适应（跟随输入）" || got["resolution"]["720P"] != "720P 高清" {
		t.Fatalf("declared values must be labelled: %v", got)
	}
	if _, ok := got["ratio"]["16:9"]; ok {
		t.Fatalf("a label equal to its value is noise and must be left out: %v", got)
	}
}

func TestValueLabelsOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(ImageSpec{Sizes: []string{"made_up"}}.WithLabels())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "value_labels") {
		t.Fatalf("value_labels should be omitted when nothing is labelled: %s", b)
	}
	b, err = json.Marshal(VideoSpec{Ratios: []string{"auto"}}.WithLabels())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"value_labels":{"ratio":{"auto":"自动"}}`) {
		t.Fatalf("value_labels wire shape changed: %s", b)
	}
}
