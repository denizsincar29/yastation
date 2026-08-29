package quasar

import (
	"encoding/json"
	"testing"
)

// TestDecodeRealisticDevicesResponse locks in the actual /m/v3/user/devices
// shape (households[].all, flat) rather than the rooms/devices nesting an
// earlier version of this code incorrectly assumed.
func TestDecodeRealisticDevicesResponse(t *testing.T) {
	raw := `{
		"status": "ok",
		"households": [
			{
				"name": "Дом",
				"all": [
					{
						"id": "dev-1",
						"name": "Станция Кухня",
						"type": "devices.types.smart_speaker.yandex.station",
						"quasar_info": {"device_id": "abc123", "platform": "yandexstation_2"},
						"capabilities": [{"type": "devices.capabilities.quasar"}]
					},
					{
						"id": "dev-2",
						"name": "Станция ТВ",
						"quasar_info": {"device_id": "def456", "platform": "saturn"},
						"capabilities": [{"type": "devices.capabilities.quasar"}]
					}
				]
			}
		]
	}`

	var data devicesResponse
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	if data.Status != "ok" {
		t.Fatalf("status = %q", data.Status)
	}
	if len(data.Households) != 1 || len(data.Households[0].All) != 2 {
		t.Fatalf("unexpected shape: %+v", data)
	}

	dev := data.Households[0].All[0]
	if dev.QuasarInfo == nil || dev.QuasarInfo.DeviceID != "abc123" {
		t.Fatalf("unexpected device: %+v", dev)
	}
	if !dev.IsSpeaker() {
		t.Fatal("expected first device to qualify as a speaker")
	}
	if data.Households[0].All[1].IsSpeaker() {
		t.Fatal("expected saturn (TV box) device to be filtered out")
	}
}

// TestScenarioJSONMatchesConfirmedWireShape encodes a scenario and checks
// the exact byte-for-byte JSON shape against what's confirmed correct in
// PROTOCOL_NOTES.md — a regression guard so a future refactor can't
// silently drift back to the wrong shape (launch_devices/phrase/etc) that
// caused device discovery to return zero speakers.
func TestScenarioJSONMatchesConfirmedWireShape(t *testing.T) {
	// ID (used for the trigger/scenario payload) deliberately differs
	// from QuasarInfo.DeviceID (a different, Glagol-only id) so this test
	// would fail if the wrong one were used, the way it was before this
	// bug was fixed against a real account.
	dev := Device{ID: "abc", Name: "Кухня", QuasarInfo: &QuasarInfo{DeviceID: "hw-should-not-be-used"}}
	trigger := dev.Trigger()

	t.Run("tts", func(t *testing.T) {
		s := buildTTSScenario("n", dev, "привет")
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}

		triggers := got["triggers"].([]any)
		trig := triggers[0].(map[string]any)["trigger"].(map[string]any)
		if trig["type"] != "scenario.trigger.voice" || trig["value"] != trigger {
			t.Fatalf("trigger = %+v", trig)
		}

		steps := got["steps"].([]any)
		step := steps[0].(map[string]any)
		if step["type"] != "scenarios.steps.actions.v2" {
			t.Fatalf("step type = %v", step["type"])
		}
		items := step["parameters"].(map[string]any)["items"].([]any)
		item := items[0].(map[string]any)
		if item["type"] != "step.action.item.device" {
			t.Fatalf("item type = %v", item["type"])
		}
		value := item["value"].(map[string]any)
		if value["item_type"] != "device" {
			t.Fatalf("item_type = %v", value["item_type"])
		}
		cap := value["capabilities"].([]any)[0].(map[string]any)
		if cap["type"] != "devices.capabilities.quasar" {
			t.Fatalf("capability type = %v", cap["type"])
		}
		state := cap["state"].(map[string]any)
		if state["instance"] != "tts" {
			t.Fatalf("instance = %v", state["instance"])
		}
		textValue, ok := state["value"].(map[string]any)
		if !ok || textValue["text"] != "привет" {
			t.Fatalf("tts value = %#v", state["value"])
		}
	})

	t.Run("command", func(t *testing.T) {
		s := buildCommandScenario("n", dev, "привет")
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		steps := got["steps"].([]any)
		item := steps[0].(map[string]any)["parameters"].(map[string]any)["items"].([]any)[0].(map[string]any)
		cap := item["value"].(map[string]any)["capabilities"].([]any)[0].(map[string]any)
		if cap["type"] != "devices.capabilities.quasar.server_action" {
			t.Fatalf("capability type = %v", cap["type"])
		}
		state := cap["state"].(map[string]any)
		if state["instance"] != "text_action" {
			t.Fatalf("instance = %v", state["instance"])
		}
		if state["value"] != "привет" {
			t.Fatalf("command value should be a plain string, got %#v", state["value"])
		}
	})

	// The batch mechanism: one scenario step whose item's
	// value.capabilities holds SEVERAL capabilities on the same device,
	// run in order by a single /actions call.
	t.Run("batch", func(t *testing.T) {
		caps := []ScenarioCapability{
			{
				Type:  "devices.capabilities.quasar",
				State: ScenarioCapabilityState{Instance: "tts", Value: map[string]string{"text": "привет"}},
			},
			{
				Type:  "devices.capabilities.quasar.server_action",
				State: ScenarioCapabilityState{Instance: "text_action", Value: "останови"},
			},
		}
		s := buildBatchScenario("n", dev, caps)
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}

		triggers := got["triggers"].([]any)
		trig := triggers[0].(map[string]any)["trigger"].(map[string]any)
		if trig["type"] != "scenario.trigger.voice" || trig["value"] != trigger {
			t.Fatalf("trigger = %+v", trig)
		}

		steps := got["steps"].([]any)
		if len(steps) != 1 {
			t.Fatalf("steps = %#v", steps)
		}
		step := steps[0].(map[string]any)
		if step["type"] != "scenarios.steps.actions.v2" {
			t.Fatalf("step type = %v", step["type"])
		}
		items := step["parameters"].(map[string]any)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items = %#v", items)
		}
		item := items[0].(map[string]any)
		if item["type"] != "step.action.item.device" {
			t.Fatalf("item type = %v", item["type"])
		}
		value := item["value"].(map[string]any)
		if value["item_type"] != "device" {
			t.Fatalf("item_type = %v", value["item_type"])
		}
		if value["id"] != dev.ID {
			t.Fatalf("value id = %v (want the scenario-system device id %q)", value["id"], dev.ID)
		}

		capsWire := value["capabilities"].([]any)
		if len(capsWire) != 2 {
			t.Fatalf("expected 2 capabilities (the batch), got %d: %#v", len(capsWire), capsWire)
		}
		first := capsWire[0].(map[string]any)
		if first["type"] != "devices.capabilities.quasar" || first["state"].(map[string]any)["instance"] != "tts" {
			t.Fatalf("first capability = %#v", first)
		}
		second := capsWire[1].(map[string]any)
		if second["type"] != "devices.capabilities.quasar.server_action" || second["state"].(map[string]any)["instance"] != "text_action" {
			t.Fatalf("second capability = %#v", second)
		}
	})
}
