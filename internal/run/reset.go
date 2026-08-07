package run

import "os/exec"

// ShellResetter runs the reset command via `sh -c` (R-CFG-21: the command is
// a user-supplied shell command, e.g. "docker compose down -v && docker
// compose up -d --wait" — it is not parsed or interpreted, just executed).
type ShellResetter struct{}

// Reset runs command and returns its error, if any (R-EXE-2: the caller
// MUST NOT proceed to load on a non-nil error).
func (ShellResetter) Reset(command string) error {
	cmd := exec.Command("sh", "-c", command)
	return cmd.Run()
}
