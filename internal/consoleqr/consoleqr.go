// Package consoleqr prints a login link to the terminal two ways at
// once: as a scannable ASCII QR code (for someone with a phone camera
// pointed at the screen) and as plain text, alone on its own line with
// nothing before it — no label, no indentation — so a screen reader
// user can jump straight to that line and copy the whole thing without
// picking through surrounding text first.
package consoleqr

import (
	"fmt"
	"io"

	qrcode "github.com/skip2/go-qrcode"
)

// Print renders link as a QR code followed by the raw link on its own
// line. If QR generation fails for some reason (shouldn't happen for a
// plain https URL, but level M has size limits), it's skipped silently —
// the link line is what actually matters and always gets printed.
func Print(w io.Writer, link string) {
	if qr, err := qrcode.New(link, qrcode.Medium); err == nil {
		fmt.Fprintln(w, qr.ToSmallString(false))
	}
	fmt.Fprintln(w, link)
}
