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
	b.WriteString("яуже ") // harmless-looking prefix, "yandex" won't collide with real commands
	for _, r := range strings.ToLower(deviceID) {
		idx := strings.IndexRune(maskHex, r)
		if idx < 0 {
			continue // skip characters we have no mapping for
		}
		b.WriteRune([]rune(maskRu)[idx])
	}
	return b.String()
}

// Household, Room, Device, Scenario mirror the small subset of the Quasar
// /user/devices and /user/scenarios response shapes that we actually use.

type QuasarInfo struct {
	DeviceID string `json:"device_id"`
	Platform string `json:"platform"`
}

type Device struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	QuasarInfo  *QuasarInfo `json:"quasar_info,omitempty"`
	SharingInfo any         `json:"sharing_info,omitempty"`
	RoomName    string      `json:"-"`
	HouseName   string      `json:"-"`
}

type Room struct {
	Name    string   `json:"name"`
	Devices []Device `json:"devices"`
}

type Household struct {
	Name  string `json:"name"`
	Rooms []Room `json:"rooms"`
	// some accounts put unassigned devices directly here
	Devices []Device `json:"devices"`
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

// IsSpeaker reports whether d looks like an Alice-capable speaker (has
// quasar_info, isn't a shared/guest device, and isn't a known
// non-speaker platform).
func (d Device) IsSpeaker() bool {
	if d.QuasarInfo == nil || d.QuasarInfo.DeviceID == "" {
		return false
	}
	if d.SharingInfo != nil {
		return false
	}
	return !nonSpeakerPlatforms[d.QuasarInfo.Platform]
}

// Trigger returns the synthetic voice-trigger phrase used to identify this
// device's dedicated scenario.
func (d Device) Trigger() string {
	return encodeDeviceTrigger(d.QuasarInfo.DeviceID)
}

type Scenario struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	IconURL  string            `json:"icon_url,omitempty"`
	Triggers []ScenarioTrigger `json:"triggers"`
	Steps    []ScenarioStep    `json:"steps"`
}

type ScenarioTrigger struct {
	Type  string `json:"type"`
	Value struct {
		Phrase string `json:"phrase"`
	} `json:"value"`
}

type ScenarioStep struct {
	Type       string           `json:"type"`
	Parameters ScenarioStepBody `json:"parameters"`
}

type ScenarioStepBody struct {
	Requested   ScenarioLaunchDevices `json:"launch_devices"`
	LaunchDelay int                   `json:"launch_delay,omitempty"`
}

type ScenarioLaunchDevices struct {
	Devices []ScenarioLaunchDevice `json:"devices"`
}

type ScenarioLaunchDevice struct {
	ID           string               `json:"id"`
	Capabilities []ScenarioCapability `json:"capabilities"`
}

type ScenarioCapability struct {
	Type  string                  `json:"type"`
	State ScenarioCapabilityState `json:"state"`
}

type ScenarioCapabilityState struct {
	Instance string `json:"instance"`
	Value    any    `json:"value"`
}

type scenariosResponse struct {
	Status    string     `json:"status"`
	Scenarios []Scenario `json:"scenarios"`
}

// buildTTSScenario builds the create/update payload for a scenario that,
// when triggered via the API, makes the device say `phrase` out loud
// (text-to-speech, `quasar.capabilities.tts`).
func buildTTSScenario(name string, dev Device, phrase string) Scenario {
	return Scenario{
		Name: name,
		Triggers: []ScenarioTrigger{{
			Type: "scenario.trigger.voice",
			Value: struct {
				Phrase string `json:"phrase"`
			}{Phrase: dev.Trigger()},
		}},
		Steps: []ScenarioStep{{
			Type: "scenarios.steps.actions",
			Parameters: ScenarioStepBody{
				Requested: ScenarioLaunchDevices{
					Devices: []ScenarioLaunchDevice{{
						ID: dev.QuasarInfo.DeviceID,
						Capabilities: []ScenarioCapability{{
							Type: "devices.capabilities.quasar.server_action",
							State: ScenarioCapabilityState{
								Instance: "phrase_action",
								Value:    phrase,
							},
						}},
					}},
				},
			},
		}},
	}
}

// buildCommandScenario is identical in shape to buildTTSScenario but sends
// the phrase as if a human had said it out loud to Alice (voice command),
// rather than as flat TTS. Yandex distinguishes the two via the capability
// instance used inside the same server_action capability type.
func buildCommandScenario(name string, dev Device, phrase string) Scenario {
	s := buildTTSScenario(name, dev, phrase)
	s.Steps[0].Parameters.Requested.Devices[0].Capabilities[0].State.Instance = "text_action"
	return s
}
