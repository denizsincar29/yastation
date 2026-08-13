package quasar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/denizsincar29/yastation/internal/glagol"
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

	// Logger receives best-effort diagnostics, e.g. when a Glagol send
	// fails and the client falls back to the slower cloud path. Defaults
	// to log.Default() if nil.
	Logger *log.Logger

	glagolMu    sync.Mutex
	glagolHosts map[string]string // device id -> "host:port"
	glagolConns map[string]*glagol.Client
	glagolTok   map[string]string
}

// NewClient wraps an already-authenticated Session.
func NewClient(sess *Session) *Client {
	return &Client{Session: sess, glagolHosts: map[string]string{}, glagolConns: map[string]*glagol.Client{}, glagolTok: map[string]string{}}
}

// EnableGlagol registers a speaker's local network address so future
// Say/Command calls to that device use the fast local Glagol WebSocket
// protocol (typically well under 100ms) instead of the cloud
// scenario-trigger fallback (~1-2s). hostPort looks like
// "192.168.1.42:1961" — find it via your router's DHCP leases or the
// official Yandex app; there's no reliable local-network discovery
// without pulling in an mDNS dependency, so this project asks you to
// supply it explicitly (see README/YASTATION_GLAGOL_HOSTS).
func (c *Client) EnableGlagol(deviceID, hostPort string) {
	c.glagolMu.Lock()
	defer c.glagolMu.Unlock()
	c.glagolHosts[deviceID] = hostPort
}

func (c *Client) logger() *log.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return log.Default()
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
		for _, d := range h.All {
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
// given trigger phrase, if any. Non-voice triggers (whose value isn't a
// plain string) are skipped rather than causing an error.
func (c *Client) findScenarioByTrigger(trigger string) string {
	for _, s := range c.Scenarios {
		for _, t := range s.Triggers {
			if phrase, ok := t.TriggerPhrase(); ok && phrase == trigger {
				return s.ID
			}
		}
	}
	return ""
}

// ensureScenario makes sure a scenario matching `build` exists for `dev`,
// creating one (via v4 scenarios) if it doesn't. Returns the scenario id.
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
		Status     string `json:"status"`
		ScenarioID string `json:"scenario_id"`
	}
	if err := decodeJSONClose(resp, &data); err != nil {
		return "", err
	}
	if data.Status != "ok" || data.ScenarioID == "" {
		return "", fmt.Errorf("не удалось создать сценарий-триггер для колонки %q", dev.Name)
	}
	build.ID = data.ScenarioID
	c.Scenarios = append(c.Scenarios, build)
	return data.ScenarioID, nil
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
		if d.ID == want || strings.EqualFold(d.Name, want) || strings.EqualFold(d.HouseName, want) {
			return d, nil
		}
		if d.QuasarInfo != nil && d.QuasarInfo.DeviceID == want {
			return d, nil
		}
	}
	return Device{}, fmt.Errorf("колонка %q не найдена", want)
}

// sendPhrase is the shared plumbing behind Say/Command: try the fast
// local Glagol path first (if a host is registered for this device via
// EnableGlagol), falling back to the cloud scenario-trigger path on any
// error (no host registered, network unreachable, device rejected the
// token, etc). Glagol only has one command shape ("sendText" — see
// PROTOCOL_NOTES.md), so tts is ignored on that path; it's preserved on
// the cloud fallback.
func (c *Client) sendPhrase(station, phrase string, tts bool) error {
	dev, err := c.selectSpeaker(station)
	if err != nil {
		return err
	}

	if hostPort, ok := c.glagolHostFor(dev.QuasarInfo.DeviceID); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.sendGlagol(ctx, dev, hostPort, phrase)
		cancel()
		if err == nil {
			return nil
		}
		c.logger().Printf("[glagol] %s: не получилось (%v), пробую через облако", dev.Name, err)
	}

	return c.sendCloud(dev, phrase, tts)
}

func (c *Client) glagolHostFor(deviceID string) (string, bool) {
	c.glagolMu.Lock()
	defer c.glagolMu.Unlock()
	h, ok := c.glagolHosts[deviceID]
	return h, ok
}

// sendGlagol sends phrase over the local WebSocket, lazily fetching a
// Glagol token and dialing the connection on first use, then reusing both
// for subsequent calls to the same device.
func (c *Client) sendGlagol(ctx context.Context, dev Device, hostPort, phrase string) error {
	c.glagolMu.Lock()
	conn, haveConn := c.glagolConns[dev.QuasarInfo.DeviceID]
	token, haveTok := c.glagolTok[dev.QuasarInfo.DeviceID]
	c.glagolMu.Unlock()

	if !haveTok {
		tok, err := glagol.GetToken(ctx, c.Session.HTTP, c.Session.XToken, dev.QuasarInfo.DeviceID, dev.QuasarInfo.Platform)
		if err != nil {
			return fmt.Errorf("токен: %w", err)
		}
		token = tok
		c.glagolMu.Lock()
		c.glagolTok[dev.QuasarInfo.DeviceID] = token
		c.glagolMu.Unlock()
	}

	if !haveConn {
		newConn, err := glagol.Dial(ctx, hostPort, token)
		if err != nil {
			return fmt.Errorf("подключение: %w", err)
		}
		conn = newConn
		c.glagolMu.Lock()
		c.glagolConns[dev.QuasarInfo.DeviceID] = conn
		c.glagolMu.Unlock()
	}

	if err := conn.SendText(ctx, phrase); err != nil {
		// connection may have gone stale (token expired, station
		// rebooted); drop it so the next call reconnects from scratch
		// instead of failing forever.
		c.glagolMu.Lock()
		delete(c.glagolConns, dev.QuasarInfo.DeviceID)
		delete(c.glagolTok, dev.QuasarInfo.DeviceID)
		c.glagolMu.Unlock()
		return err
	}
	return nil
}

// sendCloud is the original scenario-trigger path through Yandex's cloud.
func (c *Client) sendCloud(dev Device, phrase string, tts bool) error {
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

// Notify sets the volume (if volume >= 0) and then says text, matching
// the reference behaviour: a notification is a volume bump followed by a
// TTS phrase, not just TTS. Pass volume < 0 to skip the volume step.
func (c *Client) Notify(station, text string, volume float64) error {
	if volume >= 0 {
		if err := c.Volume(station, volume); err != nil {
			return err
		}
	}
	return c.Say(station, text)
}

// Volume sets the speaker volume. level is 0..10 (Alice's own native
// scale, same as the physical volume dial/voice command), not a 0..1
// fraction — clamped to that range.
func (c *Client) Volume(station string, level float64) error {
	step := int(math.Round(level))
	if step < 0 {
		step = 0
	}
	if step > 10 {
		step = 10
	}
	return c.Command(station, fmt.Sprintf("громкость на %d", step))
}

func (c *Client) Play(station string) error  { return c.Command(station, "продолжить") }
func (c *Client) Pause(station string) error { return c.Command(station, "пауза") }
func (c *Client) Stop(station string) error  { return c.Command(station, "останови") }
func (c *Client) Next(station string) error  { return c.Command(station, "следующий трек") }
func (c *Client) Previous(station string) error {
	return c.Command(station, "прошлый трек")
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
	return c.Command(station, fmt.Sprintf("напомни %s: %s", when, text))
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

// --- Experimental: raw protocol access -----------------------------------
// Everything above wraps one specific, verified shape (tts text, a
// server_action phrase, a volume 0..10 step...). Yandex's device
// capability list has more than that per PROTOCOL_NOTES.md — sound
// libraries, do-not-disturb, brightness, whatever a given speaker
// reports — none of it modeled here because none of it is confirmed
// against real hardware. Capabilities/RawCapability are the escape
// hatch: inspect what a device actually offers, then poke it directly,
// without waiting for a dedicated typed method.

// Capabilities returns the raw capabilities Yandex reported for the
// given device in the last Refresh(), exactly as received from
// /m/v3/user/devices (untouched []any — this project doesn't model the
// shape). Look here to find instance names before guessing at
// RawCapability arguments.
func (c *Client) Capabilities(station string) ([]any, error) {
	dev, err := c.selectSpeaker(station)
	if err != nil {
		return nil, err
	}
	return dev.Capabilities, nil
}

// RawCapability sends one arbitrary capability action to a device via
// the same throwaway-per-device-scenario mechanism Say/Command/Volume
// use internally (see sendCloud/ensureScenario) — just with the
// capability type/instance/value spelled out by hand instead of one of
// the built-in shapes. value is sent as-is (a string, a map, whatever
// json.Marshal makes of it).
//
// EXPERIMENTAL and UNVERIFIED beyond the two shapes this project already
// knows work ("devices.capabilities.quasar"/"tts" and
// "devices.capabilities.quasar.server_action"/"text_action" — see
// buildTTSScenario/buildCommandScenario). Any other capType/instance is
// a guess based on what Capabilities() reports; if Yandex rejects it,
// that error comes back as-is, nothing is assumed about why.
func (c *Client) RawCapability(station, capType, instance string, value any) error {
	dev, err := c.selectSpeaker(station)
	if err != nil {
		return err
	}
	build := Scenario{
		Name:     "yastation: " + dev.Name,
		Icon:     "home",
		Triggers: scenarioTrigger(dev),
		Steps: []ScenarioStep{scenarioActionStep(dev.ID, ScenarioCapability{
			Type:  capType,
			State: ScenarioCapabilityState{Instance: instance, Value: value},
		})},
	}
	id, err := c.ensureScenario(dev, "raw", build)
	if err != nil {
		return err
	}
	if err := c.updateScenarioPhrase(id, build); err != nil {
		return err
	}
	return c.runScenarioByID(id)
}
