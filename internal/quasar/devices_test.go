package quasar

import "testing"

func TestEncodeDeviceTriggerStableAndUnique(t *testing.T) {
	a := encodeDeviceTrigger("aa11-bb22")
	b := encodeDeviceTrigger("aa11-bb22")
	if a != b {
		t.Fatalf("encoding should be deterministic: %q != %q", a, b)
	}
	c := encodeDeviceTrigger("cc33-dd44")
	if a == c {
		t.Fatalf("different device ids should not collide: both encoded to %q", a)
	}
}

func TestEncodeDeviceTriggerSkipsUnknownChars(t *testing.T) {
	// device ids are hex+dashes only in practice, but make sure a stray
	// character doesn't panic and just gets skipped
	got := encodeDeviceTrigger("zz-a1")
	if got == "" {
		t.Fatal("expected non-empty trigger")
	}
}

func TestIsSpeakerFiltersNonSpeakerPlatforms(t *testing.T) {
	caps := []any{"devices.capabilities.quasar"}
	cases := []struct {
		name string
		dev  Device
		want bool
	}{
		{"no quasar info", Device{}, false},
		{"normal station", Device{QuasarInfo: &QuasarInfo{DeviceID: "1", Platform: "yandexstation_2"}, Capabilities: caps}, true},
		{"no capabilities", Device{QuasarInfo: &QuasarInfo{DeviceID: "1", Platform: "yandexstation_2"}}, false},
		{"saturn tv box", Device{QuasarInfo: &QuasarInfo{DeviceID: "1", Platform: "saturn"}, Capabilities: caps}, false},
		{"shared device", Device{QuasarInfo: &QuasarInfo{DeviceID: "1", Platform: "yandexmini"}, Capabilities: caps, SharingInfo: map[string]any{"x": 1}}, false},
	}
	for _, c := range cases {
		if got := c.dev.IsSpeaker(); got != c.want {
			t.Errorf("%s: IsSpeaker() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildScenariosDifferByInstance(t *testing.T) {
	dev := Device{Name: "Кухня", QuasarInfo: &QuasarInfo{DeviceID: "abc123"}}
	tts := buildTTSScenario("t", dev, "привет")
	cmd := buildCommandScenario("c", dev, "привет")

	ttsCap := tts.Steps[0].Parameters.Items[0].Value.Capabilities[0]
	cmdCap := cmd.Steps[0].Parameters.Items[0].Value.Capabilities[0]

	if ttsCap.State.Instance == cmdCap.State.Instance {
		t.Fatalf("tts and command scenarios should use different capability instances, both got %q", ttsCap.State.Instance)
	}
	if ttsCap.Type != "devices.capabilities.quasar" {
		t.Fatalf("tts capability type = %q, want devices.capabilities.quasar", ttsCap.Type)
	}
	if cmdCap.Type != "devices.capabilities.quasar.server_action" {
		t.Fatalf("command capability type = %q, want devices.capabilities.quasar.server_action", cmdCap.Type)
	}
	if _, ok := ttsCap.State.Value.(map[string]string); !ok {
		t.Fatalf("tts value should be a {\"text\":...} object, got %#v", ttsCap.State.Value)
	}
	if _, ok := cmdCap.State.Value.(string); !ok {
		t.Fatalf("command value should be a plain string, got %#v", cmdCap.State.Value)
	}
	if tts.Triggers[0].Trigger.Value != cmd.Triggers[0].Trigger.Value {
		t.Fatalf("tts and command scenario for the same device should share the same trigger phrase")
	}
}

func TestBuildScenarioMatchesWireShape(t *testing.T) {
	// Golden-shape check against the confirmed real wire format (see
	// PROTOCOL_NOTES.md): a "trigger" wrapper key, steps.actions.v2,
	// items[0].value.id / item_type / capabilities.
	dev := Device{QuasarInfo: &QuasarInfo{DeviceID: "dev1"}}
	s := buildCommandScenario("n", dev, "привет")

	if len(s.Triggers) != 1 || s.Triggers[0].Trigger.Type != "scenario.trigger.voice" {
		t.Fatalf("unexpected triggers: %+v", s.Triggers)
	}
	if len(s.Steps) != 1 || s.Steps[0].Type != "scenarios.steps.actions.v2" {
		t.Fatalf("unexpected step type: %+v", s.Steps)
	}
	items := s.Steps[0].Parameters.Items
	if len(items) != 1 || items[0].ID != "dev1" || items[0].Type != "step.action.item.device" {
		t.Fatalf("unexpected items: %+v", items)
	}
	if items[0].Value.ID != "dev1" || items[0].Value.ItemType != "device" {
		t.Fatalf("unexpected item value: %+v", items[0].Value)
	}
}
