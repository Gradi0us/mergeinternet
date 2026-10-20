// Package deploy embeds the VPS installer script so the app can upload it over SSH.
package deploy

import _ "embed"

// ServerSetup is the content of server-setup.sh.
//
//go:embed server-setup.sh
var ServerSetup []byte
