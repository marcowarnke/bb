// Package skills embeds the Claude Code skills shipped with bb, so
// `bb skills install` always installs the ones matching the binary.
package skills

import "embed"

//go:embed */SKILL.md
var FS embed.FS
