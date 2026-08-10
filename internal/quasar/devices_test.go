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
	cases := []struct {
		name string
		dev  Device
		want bool
	}{
		{"no quasar info", Device{}, false},
		{"normal station", Device{QuasarInfo: &QuasarInfo{DeviceID: "1", Platform: "yandexstation_2"}}, true},
		{"saturn tv box", Device{QuasarInfo: &QuasarInfo{DeviceID: "1", Platform: "saturn"}}, false},
		{"shared device", Device{QuasarInfo: &QuasarInfo{DeviceID: "1", Platform: "yandexmini"}, SharingInfo: map[string]any{"x": 1}}, false},
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

	ttsInstance := tts.Steps[0].Parameters.Requested.Devices[0].Capabilities[0].State.Instance
	cmdInstance := cmd.Steps[0].Parameters.Requested.Devices[0].Capabilities[0].State.Instance
	if ttsInstance == cmdInstance {
		t.Fatalf("tts and command scenarios should use different capability instances, both got %q", ttsInstance)
	}
	if tts.Triggers[0].Value.Phrase != cmd.Triggers[0].Value.Phrase {
		t.Fatalf("tts and command scenario for the same device should share the same trigger phrase")
	}
}
