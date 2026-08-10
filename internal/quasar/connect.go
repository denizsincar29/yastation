package quasar

import "os"

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
	return c, nil
}
