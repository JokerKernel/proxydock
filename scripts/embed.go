// Package scripts embeds the same installer used by the one-command setup.
package scripts

import _ "embed"

// Installer is included at build time, so CLI management runs reviewed local
// code and downloads only release metadata and binaries.
//
//go:embed install.sh
var Installer string
