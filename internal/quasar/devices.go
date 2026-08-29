package quasar

import "strings"

// Quasar requires every voice scenario to have a launch phrase. To send a
// silent, per-device command we still need a scenario with *some* unique
// phrase attached to it — we never actually speak it, we trigger the
// scenario directly via its /actions endpoint. To keep the phrase stable
// and unique per device without colliding with real Alice vocabulary, we
// map the device id's hex characters onto a fixed set of Cyrillic letters.
const maskHex = "0123456789abcdef-"
const maskRu = "оеаинтсрвлкмдпуяы"

// encodeDeviceTrigger turns a device id into a pronounceable-looking but
// practically never-said Russian phrase, unique per device id.
func encodeDeviceTrigger(deviceID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(deviceID) {
		idx := strings.IndexRune(maskHex, r)
		if idx < 0 {
			continue // skip characters we have no mapping for
		}
		b.WriteRune([]rune(maskRu)[idx])
	}
	return b.String()
}

// QuasarInfo, Device, Household, Scenario mirror the subset of the Quasar
// /user/devices and /user/scenarios response shapes that we actually use.

type QuasarInfo struct {
	DeviceID string `json:"device_id"`
	Platform string `json:"platform"`
}

type Device struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Type         string      `json:"type"`
	QuasarInfo   *QuasarInfo `json:"quasar_info,omitempty"`
	SharingInfo  any         `json:"sharing_info,omitempty"`
	Capabilities []any       `json:"capabilities,omitempty"`
	HouseName    string      `json:"-"`
}

// Household mirrors the actual /m/v3/user/devices response shape: every
// device belonging to the household (regardless of room) is listed flat
// under "all" — there's no separate rooms/devices nesting to walk.
type Household struct {
	Name string   `json:"name"`
	All  []Device `json:"all"`
}

type devicesResponse struct {
	Status     string      `json:"status"`
	Households []Household `json:"households"`
}

// nonSpeakerPlatforms are quasar_info.platform values that are smart-home
// devices, not Alice speakers (a game console, a TV box, etc.) and should
// not show up as targets for voice commands.
var nonSpeakerPlatforms = map[string]bool{
	"saturn": true, // "Яндекс.ТВ Станция" set-top box, not a speaker
	"mike":   true, // Bluetooth-only adapter, no mic
	"cherry": true, // module/OEM variant, non-interactive
}

// IsSpeaker reports whether d looks like an Alice-capable speaker: has
// quasar_info, isn't a shared/guest device, isn't a known non-speaker
// platform, and actually exposes capabilities (some "ghost" devices show
// up with quasar_info but no capabilities and aren't controllable).
func (d Device) IsSpeaker() bool {
	if d.QuasarInfo == nil || d.QuasarInfo.DeviceID == "" {
		return false
	}
	if d.SharingInfo != nil {
		return false
	}
	if len(d.Capabilities) == 0 {
		return false
	}
	return !nonSpeakerPlatforms[d.QuasarInfo.Platform]
}

// Trigger returns the synthetic voice-trigger phrase used to identify
// this device's dedicated scenario. Built from the device's own top-level
// id (the smart-home/IoT platform id used by the scenario system), not
// quasar_info.device_id (a different, hardware-level id used only by the
// separate local Glagol protocol) — using the wrong one here is what
// caused Yandex to reject scenario creation with DEVICE_NOT_FOUND.
func (d Device) Trigger() string {
	return encodeDeviceTrigger(d.ID)
}

type Scenario struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Icon     string            `json:"icon,omitempty"`
	Triggers []ScenarioTrigger `json:"triggers"`
	Steps    []ScenarioStep    `json:"steps"`
}

// ScenarioTrigger matches the wire shape exactly: a single "trigger" key
// wrapping type+value. Value is `any`, not a fixed string, because
// non-voice triggers (time-based, etc — real accounts have these from
// the official app, not just the ones this project creates) carry
// non-string values here; only voice triggers have a plain string
// phrase. See TriggerPhrase().
type ScenarioTrigger struct {
	Trigger ScenarioTriggerBody `json:"trigger"`
}

type ScenarioTriggerBody struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// TriggerPhrase returns the trigger's value as a string, and false if
// this trigger isn't a plain-string voice trigger (e.g. a time trigger,
// whose value is an object).
func (t ScenarioTrigger) TriggerPhrase() (string, bool) {
	s, ok := t.Trigger.Value.(string)
	return s, ok
}

type ScenarioStep struct {
	Type       string                 `json:"type"`
	Parameters ScenarioStepParameters `json:"parameters"`
}

type ScenarioStepParameters struct {
	Items []ScenarioActionItem `json:"items"`
}

type ScenarioActionItem struct {
	ID    string                  `json:"id"`
	Type  string                  `json:"type"` // "step.action.item.device"
	Value ScenarioActionItemValue `json:"value"`
}

type ScenarioActionItemValue struct {
	ID           string               `json:"id"`
	ItemType     string               `json:"item_type"` // "device"
	Capabilities []ScenarioCapability `json:"capabilities"`
}

type ScenarioCapability struct {
	Type  string                  `json:"type"`
	State ScenarioCapabilityState `json:"state"`
}

// State.Value differs by capability: a plain string for a
// server_action/text_action command, or {"text": "..."} for tts.
type ScenarioCapabilityState struct {
	Instance string `json:"instance"`
	Value    any    `json:"value"`
}

type scenariosResponse struct {
	Status    string     `json:"status"`
	Scenarios []Scenario `json:"scenarios"`
}

func scenarioActionStep(deviceID string, capability ScenarioCapability) ScenarioStep {
	return ScenarioStep{
		Type: "scenarios.steps.actions.v2",
		Parameters: ScenarioStepParameters{
			Items: []ScenarioActionItem{{
				ID:   deviceID,
				Type: "step.action.item.device",
				Value: ScenarioActionItemValue{
					ID:           deviceID,
					ItemType:     "device",
					Capabilities: []ScenarioCapability{capability},
				},
			}},
		},
	}
}

func scenarioTrigger(dev Device) []ScenarioTrigger {
	return []ScenarioTrigger{{Trigger: ScenarioTriggerBody{
		Type:  "scenario.trigger.voice",
		Value: dev.Trigger(),
	}}}
}

// buildTTSScenario builds the create/update payload for a scenario that,
// when triggered via the API, makes the device say `text` out loud
// (text-to-speech). Uses dev.ID (the IoT/scenario-system device id), not
// quasar_info.device_id.
func buildTTSScenario(name string, dev Device, text string) Scenario {
	return Scenario{
		Name:     name,
		Icon:     "home",
		Triggers: scenarioTrigger(dev),
		Steps: []ScenarioStep{scenarioActionStep(dev.ID, ScenarioCapability{
			Type:  "devices.capabilities.quasar",
			State: ScenarioCapabilityState{Instance: "tts", Value: map[string]string{"text": text}},
		})},
	}
}

// MaxTTSChunkChars caps a single tts phrase inside a cloud scenario. The
// limit is measured live, not guessed: Yandex rejects a tts step longer
// than 100 symbols with QUASAR_SERVER_ACTION_LENGTH_ERROR ("Команда Алисе
// должна быть не длиннее 100 символов"). 96 leaves a small margin.
// Longer text is split before batching (see internal/app.splitSpeech), so
// this is the one knob to turn if a phrase ever fails to play because it's
// too long.
const MaxTTSChunkChars = 96

// buildBatchScenario is a scenario that runs several capabilities on one
// device back to back — the batching mechanism Client.Batch uses. Each
// action is its own step: the wire shape is confirmed against a live
// device — a single step carrying several capabilities in one item is
// rejected (HTTP 400), and one device repeated inside a single step is
// rejected too ("Нельзя добавить одно и то же устройство в один шаг
// сценария несколько раз"). Yandex runs the steps in order, Alice
// finishing each action before the next starts (no delay between them).
func buildBatchScenario(name string, dev Device, caps []ScenarioCapability) Scenario {
	steps := make([]ScenarioStep, 0, len(caps))
	for _, cap := range caps {
		steps = append(steps, scenarioActionStep(dev.ID, cap))
	}
	return Scenario{
		Name:     name,
		Icon:     "home",
		Triggers: scenarioTrigger(dev),
		Steps:    steps,
	}
}

// buildCommandScenario is the same shape but sends the phrase as if a
// human had said it out loud to Alice (voice command) rather than as flat
// TTS.
func buildCommandScenario(name string, dev Device, action string) Scenario {
	return Scenario{
		Name:     name,
		Icon:     "home",
		Triggers: scenarioTrigger(dev),
		Steps: []ScenarioStep{scenarioActionStep(dev.ID, ScenarioCapability{
			Type:  "devices.capabilities.quasar.server_action",
			State: ScenarioCapabilityState{Instance: "text_action", Value: action},
		})},
	}
}
