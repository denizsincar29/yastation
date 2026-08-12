package quasar

import (
	"fmt"
	"time"
)

// LoginViaQR runs the full QR-login flow: start it, hand the link to
// onLink (so the caller can print/show a QR code — GetQR already handed
// this back before it was wrapped here), then poll until the user
// confirms on their phone/browser or timeout passes.
//
// Nothing about this needs a browser or a listening HTTP server on the
// machine running it — the link is meant to be opened on a *different*
// device (the account owner's phone), so this works exactly the same
// over SSH with no local browser as it does on a desktop. That's what
// makes it reusable for cmd/yastation-access: an admin can run it on a
// headless server to onboard someone else's account, as long as that
// person is the one opening the link and confirming.
func LoginViaQR(timeout time.Duration, onLink func(link string)) (*Session, error) {
	sess, err := NewSession()
	if err != nil {
		return nil, err
	}
	link, err := sess.GetQR()
	if err != nil {
		return nil, err
	}
	if onLink != nil {
		onLink(link)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := sess.LoginQR()
		if err != nil {
			return nil, err
		}
		if result != nil {
			return sess, nil
		}
		time.Sleep(3 * time.Second)
	}
	return nil, fmt.Errorf("время ожидания QR истекло")
}
