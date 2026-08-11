// Package glagol talks directly to an Alice speaker over the local
// network, instead of going through Yandex's cloud (which is what
// internal/quasar's scenario-trigger trick does, and why it takes ~1-2s
// round trip per command). Glagol is the same local WebSocket protocol
// the official Yandex app and AlexxIT/YandexStation (MIT) use — this is
// again an independent implementation, not copied code, see
// PROTOCOL_NOTES.md for exactly what's confirmed vs reconstructed.
package glagol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// GetToken exchanges an account x-token for a short-lived, device-specific
// local token that the device itself will accept over the Glagol
// WebSocket. httpClient can be any *http.Client — it does not need
// cookies or CSRF, just the OAuth header, so callers don't have to reuse
// quasar.Session for this (kept dependency-free from the quasar package
// on purpose, to avoid an import cycle).
func GetToken(ctx context.Context, httpClient *http.Client, xToken, deviceID, platform string) (string, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	u := "https://quasar.yandex.net/glagol/token?" + url.Values{
		"device_id": {deviceID},
		"platform":  {platform},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "OAuth "+xToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var data struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("glagol token: невалидный ответ: %w", err)
	}
	if data.Status != "ok" || data.Token == "" {
		return "", fmt.Errorf("glagol token: яндекс вернул статус %q", data.Status)
	}
	return data.Token, nil
}
