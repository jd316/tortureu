package main

import (
	"os/exec"
	"strings"
)

// PrereqCheck is one prerequisite `run` needs, checked by presence only
// (exec.LookPath, plus a best-effort version string) — never by guessing a
// path, and never claiming a tool "works" beyond "the binary is on PATH":
// presence is what can be checked, so presence is what is claimed.
type PrereqCheck struct {
	Name    string
	Found   bool
	Version string // best-effort, empty when not found or not cheaply obtainable
	Hint    string // how to get it; only meaningful when !Found
}

// checkPrerequisites checks the three things a real `run` needs: k6 (the
// load engine), docker, and docker compose (the R-DC2-3 topology overlay
// and the reset command's default). Nothing here is mocked in tests — it
// is the real exec.LookPath against the process's real PATH — so a test
// proves genuine absence by pointing PATH at an empty directory, not by
// stubbing the check away.
func checkPrerequisites() []PrereqCheck {
	k6 := checkBinary("k6", []string{"version"},
		"install: https://k6.io/docs/get-started/installation/")
	docker := checkBinary("docker", []string{"--version"},
		"install: https://docs.docker.com/get-docker/")
	compose := checkDockerCompose(docker.Found)
	return []PrereqCheck{k6, docker, compose}
}

// checkBinary reports whether name is on PATH, and if so, a best-effort
// first line of `name <versionArgs...>` output. A version command that
// fails or hangs is not treated as "not found" — LookPath already answered
// that question; a version string is a bonus, not the check.
func checkBinary(name string, versionArgs []string, hint string) PrereqCheck {
	if _, err := exec.LookPath(name); err != nil {
		return PrereqCheck{Name: name, Found: false, Hint: hint}
	}
	out, err := exec.Command(name, versionArgs...).CombinedOutput()
	version := ""
	if err == nil {
		version = firstLine(string(out))
	}
	return PrereqCheck{Name: name, Found: true, Version: version}
}

// checkDockerCompose checks the `docker compose` plugin subcommand, not a
// separate binary on PATH (the standalone docker-compose v1 binary is
// deprecated upstream). It is trivially absent if docker itself is absent.
func checkDockerCompose(dockerFound bool) PrereqCheck {
	const hint = "install: https://docs.docker.com/compose/install/ (bundled with modern Docker Desktop/Engine)"
	if !dockerFound {
		return PrereqCheck{Name: "docker compose", Found: false, Hint: hint}
	}
	out, err := exec.Command("docker", "compose", "version").CombinedOutput()
	if err != nil {
		return PrereqCheck{Name: "docker compose", Found: false, Hint: hint}
	}
	return PrereqCheck{Name: "docker compose", Found: true, Version: firstLine(string(out))}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// missingPrerequisites filters checks down to what's absent.
func missingPrerequisites(checks []PrereqCheck) []PrereqCheck {
	var out []PrereqCheck
	for _, c := range checks {
		if !c.Found {
			out = append(out, c)
		}
	}
	return out
}
