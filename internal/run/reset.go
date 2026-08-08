package run

import (
	"fmt"
	"os/exec"
	"strings"
)

// ShellResetter runs the reset command via `sh -c` (R-CFG-21: the command is
// a user-supplied shell command, e.g. "docker compose down -v && docker
// compose up -d --wait" — it is not parsed or interpreted, just executed).
type ShellResetter struct{}

// Reset runs command and returns its error, if any (R-EXE-2: the caller
// MUST NOT proceed to load on a non-nil error).
func (ShellResetter) Reset(command string) error {
	// CombinedOutput, not Run: a failed reset is almost always explained
	// only by the command's own output — a missing secret file, a bound
	// port, an image that will not pull — and Run() discards it, leaving
	// "exit status 1" as the whole diagnosis (R-VER-16).
	out, err := exec.Command("sh", "-c", command).CombinedOutput()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, lastLines(detail, 8))
}
