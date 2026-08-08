package run

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// commitAnchor resolves the git HEAD of the repository containing
// composePath (R-VER-12), as the full 40-character hash.
//
// The full hash, not an abbreviation: VERDICT.md §4 shortens it for
// display, but the JSON document is what a trend store reads, and
// `bencher run --hash` rejects anything shorter outright.
//
// Every failure — not a checkout, no git, unresolvable HEAD — returns "".
// An invented anchor is worse than an absent one, because a trend keyed on
// a fabricated commit silently compares runs that are not what it says
// they are.
func commitAnchor(composePath string) string {
	dir := filepath.Dir(composePath)
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	hash := strings.TrimSpace(string(out))
	if len(hash) != 40 {
		return ""
	}
	return hash
}
