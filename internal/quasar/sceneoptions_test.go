package quasar

import (
	"encoding/json"
	"testing"
)

// realCapabilitiesDump is trimmed from an actual /capabilities output on
// real hardware (see PROTOCOL_NOTES.md) — used to make sure SceneOptions
// parses the real shape, not a shape we assumed.
const realCapabilitiesDump = `[
  {
    "parameters": {
      "custom_palette": null,
      "custom_scenes": null,
      "custom_scenes_available": false,
      "instance": "color",
      "name": "цвет",
      "palette": [],
      "scenes": [
        {"id": "lava_lamp", "name": "Лава лампа"},
        {"id": "inactive", "name": "Неактивный"},
        {"id": "night", "name": "Ночь"},
        {"id": "candle", "name": "Свеча"}
      ]
    },
    "reportable": false,
    "retrievable": false,
    "state": {"instance": "scene", "value": {"id": "lava_lamp", "name": "Лава лампа"}},
    "type": "devices.capabilities.color_setting"
  },
  {
    "parameters": {"instance": "text_action"},
    "reportable": false,
    "retrievable": false,
    "state": null,
    "type": "devices.capabilities.quasar.server_action"
  },
  {
    "parameters": {"instance": "volume"},
    "reportable": false,
    "retrievable": false,
    "state": {"instance": "volume", "value": {"value": 3}},
    "type": "devices.capabilities.quasar"
  }
]`

func decodeCaps(t *testing.T, raw string) []any {
	t.Helper()
	var caps []any
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		t.Fatal(err)
	}
	return caps
}

func TestSceneOptionsParsesRealDump(t *testing.T) {
	caps := decodeCaps(t, realCapabilitiesDump)
	opts, ok := SceneOptions(caps)
	if !ok {
		t.Fatal("expected ok=true, device has a color_setting/scenes capability")
	}
	want := map[string]string{
		"lava_lamp": "Лава лампа",
		"inactive":  "Неактивный",
		"night":     "Ночь",
		"candle":    "Свеча",
	}
	if len(opts) != len(want) {
		t.Fatalf("opts=%v", opts)
	}
	for _, o := range opts {
		if want[o.ID] != o.Name {
			t.Fatalf("opt %+v doesn't match expected name %q", o, want[o.ID])
		}
	}
}

func TestSceneOptionsNoColorCapabilityReturnsNotOK(t *testing.T) {
	caps := decodeCaps(t, `[{"type": "devices.capabilities.quasar", "parameters": {"instance": "volume"}}]`)
	_, ok := SceneOptions(caps)
	if ok {
		t.Fatal("expected ok=false when there's no color_setting capability at all")
	}
}

func TestSceneOptionsEmptyScenesListStillOK(t *testing.T) {
	caps := decodeCaps(t, `[{"type": "devices.capabilities.color_setting", "parameters": {"instance": "color", "scenes": []}}]`)
	opts, ok := SceneOptions(caps)
	if !ok {
		t.Fatal("expected ok=true — the capability exists, it just has no scenes")
	}
	if len(opts) != 0 {
		t.Fatalf("opts=%v", opts)
	}
}

func TestSceneOptionsMalformedEntriesAreSkippedNotPanicked(t *testing.T) {
	caps := []any{
		"not a map",
		42,
		map[string]any{"type": "devices.capabilities.color_setting", "parameters": "not a map either"},
	}
	if _, ok := SceneOptions(caps); ok {
		t.Fatal("expected ok=false for garbage input")
	}
}
