// Package yastation holds the one repo-root asset that needs to be
// embedded into every binary (cmd/yastation, cmd/yastation-server): the
// default set of template-based commands (play/pause/timer/... — see
// config.json.default). Everything else lives under internal/ and cmd/;
// this file exists purely because go:embed can't reach outside the
// package directory it's declared in, and config.json.default belongs at
// the repo root where it's easy to find and edit.
package yastation

import _ "embed"

// DefaultCommandsJSON is the raw content of config.json.default, parsed
// by internal/app.DefaultCommandsConfig and copied to a user-editable
// config.json on first run (see internal/app.EnsureConfigFile).
//
//go:embed config.json.default
var DefaultCommandsJSON []byte
