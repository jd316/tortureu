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
	return fmt.Errorf("%w: %s", err, resetCause(detail))
}

// resetCause reduces a reset command's output to the part that explains the
// failure. `docker compose up` prints a progress line per container and then
// the reason; relaying the raw tail buried the reason in "Container X
// Created" noise on the one line a user reads first (R-VER-16).
//
// Lines that announce a failure are preferred. When none match — a command
// that fails silently, or one whose wording we do not recognise — the tail
// is still relayed, because some output beats none.
func resetCause(out string) string {
	var causes []string
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if strings.Contains(low, "error") || strings.Contains(low, "failed") ||
			strings.Contains(low, "cannot") || strings.Contains(low, "denied") ||
			strings.Contains(low, "no such") || strings.Contains(low, "not available") {
			causes = append(causes, t)
		}
	}
	if len(causes) == 0 {
		return lastLines(out, 8)
	}
	// Duplicates are common: compose repeats a daemon error per attempt.
	seen := map[string]bool{}
	var uniq []string
	for _, c := range causes {
		if seen[c] {
			continue
		}
		seen[c] = true
		uniq = append(uniq, c)
	}
	if len(uniq) > 4 {
		uniq = uniq[len(uniq)-4:]
	}
	return strings.Join(uniq, "; ")
}
