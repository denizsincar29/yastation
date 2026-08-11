package quasar

import (
	"os"
	"strings"
)

// Connect loads saved tokens from disk, makes sure the session is still
// valid, and refreshes the device/scenario list. Callers that haven't
// authenticated yet should run the QR flow (see cmd/yastation-auth) before
// calling this.
func Connect() (*Client, error) {
	sess, err := LoadTokens()
	if err != nil {
		return nil, err
	}
	if _, err := sess.RefreshCookies(); err != nil {
		return nil, err
	}
	c := NewClient(sess)
	c.DefaultDeviceID = os.Getenv("YASTATION_STATION_ID")
	c.DefaultDeviceName = os.Getenv("YASTATION_STATION_NAME")
	if err := c.Refresh(); err != nil {
		return nil, err
	}
	for id, hostPort := range ParseGlagolHosts(os.Getenv("YASTATION_GLAGOL_HOSTS")) {
		c.EnableGlagol(id, hostPort)
	}
	return c, nil
}

// ParseGlagolHosts parses the YASTATION_GLAGOL_HOSTS format:
// "deviceid1=host1:port1,deviceid2=host2:port2". Blank entries and
// whitespace are tolerated; malformed entries (missing "=") are skipped.
func ParseGlagolHosts(spec string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		id, hostPort, ok := strings.Cut(pair, "=")
		id, hostPort = strings.TrimSpace(id), strings.TrimSpace(hostPort)
		if !ok || id == "" || hostPort == "" {
			continue
		}
		out[id] = hostPort
	}
	return out
}
