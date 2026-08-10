package quasar

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client is the main entry point of the library: an authenticated Quasar
// session plus the set of discovered speakers and their per-device
// "trigger" scenarios.
type Client struct {
	Session *Session

	Speakers  []Device
	Scenarios []Scenario

	// DefaultDeviceID / DefaultDeviceName pick which speaker receives
	// commands when the caller doesn't specify one explicitly.
	DefaultDeviceID   string
	DefaultDeviceName string
}

// NewClient wraps an already-authenticated Session.
func NewClient(sess *Session) *Client {
	return &Client{Session: sess}
}

// Refresh reloads the device list and scenario list from Yandex, and makes
// sure every discovered speaker has a dedicated trigger scenario it can
// use to receive commands.
func (c *Client) Refresh() error {
	if err := c.loadDevices(); err != nil {
		return fmt.Errorf("список устройств: %w", err)
	}
	if err := c.loadScenarios(); err != nil {
		return fmt.Errorf("список сценариев: %w", err)
	}
	return nil
}

func (c *Client) loadDevices() error {
	resp, err := c.Session.Request(http.MethodGet, "https://iot.quasar.yandex.ru/m/v3/user/devices", nil)
	if err != nil {
		return err
	}
	var data devicesResponse
	if err := decodeJSONClose(resp, &data); err != nil {
		return err
	}
	if data.Status != "ok" {
		return fmt.Errorf("яндекс вернул статус %q при получении устройств", data.Status)
	}

	var speakers []Device
	for _, h := range data.Households {
		for _, r := range h.Rooms {
			for _, d := range r.Devices {
				d.RoomName, d.HouseName = r.Name, h.Name
				if d.IsSpeaker() {
					speakers = append(speakers, d)
				}
			}
		}
		for _, d := range h.Devices {
			d.HouseName = h.Name
			if d.IsSpeaker() {
				speakers = append(speakers, d)
			}
		}
	}
	c.Speakers = speakers
	if len(speakers) == 0 {
		return fmt.Errorf("на аккаунте не найдено ни одной колонки с Алисой")
	}
	return nil
}

func (c *Client) loadScenarios() error {
	resp, err := c.Session.Request(http.MethodGet, "https://iot.quasar.yandex.ru/m/user/scenarios", nil)
	if err != nil {
		return err
	}
	var data scenariosResponse
	if err := decodeJSONClose(resp, &data); err != nil {
		return err
	}
	if data.Status != "ok" {
		return fmt.Errorf("яндекс вернул статус %q при получении сценариев", data.Status)
	}
	c.Scenarios = data.Scenarios
	return nil
}

// findScenarioByTrigger returns the scenario id already wired up to the
// given trigger phrase, if any.
func (c *Client) findScenarioByTrigger(trigger string) string {
	for _, s := range c.Scenarios {
		for _, t := range s.Triggers {
			if t.Value.Phrase == trigger {
				return s.ID
			}
		}
	}
	return ""
}

// ensureScenario makes sure a scenario matching `build` exists for `dev`,
// creating one (via v4 scenarios) or updating it in place if the phrase
// changed. Returns the scenario id.
func (c *Client) ensureScenario(dev Device, name string, build Scenario) (string, error) {
	trigger := dev.Trigger()
	if id := c.findScenarioByTrigger(trigger); id != "" {
		return id, nil
	}

	resp, err := c.Session.Request(http.MethodPost, "https://iot.quasar.yandex.ru/m/v4/user/scenarios", build)
	if err != nil {
		return "", err
	}
	var data struct {
		Status   string `json:"status"`
		Scenario struct {
			ID string `json:"id"`
		} `json:"scenario"`
	}
	if err := decodeJSONClose(resp, &data); err != nil {
		return "", err
	}
	if data.Status != "ok" || data.Scenario.ID == "" {
		return "", fmt.Errorf("не удалось создать сценарий-триггер для колонки %q", dev.Name)
	}
	c.Scenarios = append(c.Scenarios, build)
	return data.Scenario.ID, nil
}

// runScenarioByID fires a scenario's actions immediately, without anyone
// having said its trigger phrase out loud.
func (c *Client) runScenarioByID(id string) error {
	resp, err := c.Session.Request(http.MethodPost,
		fmt.Sprintf("https://iot.quasar.yandex.ru/m/user/scenarios/%s/actions", id), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

// selectSpeaker resolves which device a command should target: an
// explicit name/id if given, otherwise DefaultDeviceID/Name, otherwise the
// first discovered speaker.
func (c *Client) selectSpeaker(nameOrID string) (Device, error) {
	if len(c.Speakers) == 0 {
		return Device{}, fmt.Errorf("список колонок пуст, вызови Refresh()")
	}
	want := nameOrID
	if want == "" {
		want = c.DefaultDeviceID
	}
	if want == "" {
		want = c.DefaultDeviceName
	}
	if want == "" {
		return c.Speakers[0], nil
	}
	for _, d := range c.Speakers {
		if d.ID == want || strings.EqualFold(d.Name, want) {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("колонка %q не найдена", want)
}

// sendPhrase is the shared plumbing behind Say/Command: make sure the
// device has a trigger scenario, then fire it with the given phrase baked
// into the action payload.
func (c *Client) sendPhrase(station, phrase string, tts bool) error {
	dev, err := c.selectSpeaker(station)
	if err != nil {
		return err
	}
	kind := "command"
	build := buildCommandScenario("yastation: "+dev.Name, dev, phrase)
	if tts {
		kind = "tts"
		build = buildTTSScenario("yastation: "+dev.Name, dev, phrase)
	}
	id, err := c.ensureScenario(dev, kind, build)
	if err != nil {
		return err
	}
	// The phrase is baked in at scenario-creation time above; if the
	// scenario already existed with a *different* phrase we need to patch
	// it before running it.
	if err := c.updateScenarioPhrase(id, build); err != nil {
		return err
	}
	return c.runScenarioByID(id)
}

func (c *Client) updateScenarioPhrase(id string, build Scenario) error {
	resp, err := c.Session.Request(http.MethodPut,
		"https://iot.quasar.yandex.ru/m/v4/user/scenarios/"+id, build)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

// --- High level actions ---------------------------------------------------
// These build a Russian phrase the same way a person would say it to
// Alice, then push it through sendPhrase. Kept as small, obvious string
// templates so they're easy to tweak (Alice's own phrasing sensitivity
// changes over time).

func (c *Client) Say(station, text string) error { return c.sendPhrase(station, text, true) }

func (c *Client) Command(station, text string) error { return c.sendPhrase(station, text, false) }

func (c *Client) Notify(station, text string) error {
	return c.Say(station, text)
}

func (c *Client) Volume(station string, level float64) error {
	// Alice understands 0..10 steps, not 0..1 fractions.
	step := int(level * 10)
	if step < 0 {
		step = 0
	}
	if step > 10 {
		step = 10
	}
	return c.Command(station, fmt.Sprintf("громкость на %d", step))
}

func (c *Client) Play(station string) error  { return c.Command(station, "продолжи") }
func (c *Client) Pause(station string) error { return c.Command(station, "пауза") }
func (c *Client) Stop(station string) error  { return c.Command(station, "останови") }
func (c *Client) Next(station string) error  { return c.Command(station, "следующий трек") }
func (c *Client) Previous(station string) error {
	return c.Command(station, "предыдущий трек")
}

func (c *Client) Timer(station string, minutes int, label string) error {
	phrase := fmt.Sprintf("поставь таймер на %d минут", minutes)
	if label != "" {
		phrase += " " + label
	}
	return c.Command(station, phrase)
}

func (c *Client) Alarm(station, atTime, label string) error {
	phrase := fmt.Sprintf("поставь будильник на %s", atTime)
	if label != "" {
		phrase += " " + label
	}
	return c.Command(station, phrase)
}

func (c *Client) Reminder(station, text, when string) error {
	return c.Command(station, fmt.Sprintf("напомни %s %s", when, text))
}

func (c *Client) Weather(station string) error { return c.Command(station, "какая погода") }
func (c *Client) News(station string) error    { return c.Command(station, "включи новости") }

// RunScenario runs one of the user's *own* smart-home scenarios by name
// (not a yastation trigger scenario).
func (c *Client) RunScenario(name string) error {
	for _, s := range c.Scenarios {
		if strings.EqualFold(s.Name, name) {
			return c.runScenarioByID(s.ID)
		}
	}
	return fmt.Errorf("сценарий %q не найден", name)
}

// ListScenarios returns names of the user's own scenarios, excluding the
// internal per-device trigger scenarios this library creates.
func (c *Client) ListScenarios() []string {
	var out []string
	for _, s := range c.Scenarios {
		if strings.HasPrefix(s.Name, "yastation: ") {
			continue
		}
		out = append(out, s.Name)
	}
	return out
}

// Diagnostics returns a small human-readable status blob, mirroring what
// the Python REPL's /stations command used to print.
func (c *Client) Diagnostics() (string, error) {
	dev, err := c.selectSpeaker("")
	if err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(map[string]any{
		"default_station": map[string]string{"name": dev.Name, "platform": dev.QuasarInfo.Platform},
		"speakers_count":  len(c.Speakers),
	}, "", "  ")
	return string(b), nil
}
