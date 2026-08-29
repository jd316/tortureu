package run

import (
	"errors"
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/detect"
)

var errResetBoom = errors.New("docker compose: boom")

// spec: R-VER-16
//
// ShellResetter used cmd.Run(), which discards stdout and stderr, so the
// only thing that could explain a failed reset was thrown away before the
// caller saw it.
func TestShellResetter_ErrorCarriesTheCommandOutput(t *testing.T) {
	err := ShellResetter{}.Reset("echo 'bind source path does not exist: db/password.txt' >&2; exit 1")
	if err == nil {
		t.Fatal("want an error from a failing reset command")
	}
	if !strings.Contains(err.Error(), "db/password.txt") {
		t.Errorf("error does not carry the command's own output, which is the only thing that explains it: %v", err)
	}
}

// spec: R-VER-16
//
// A reset that fails must abort with the reason in the verdict's error
// field. It rendered "reset: failed" with "error": null, on a user's very
// first run.
func TestRun_AbortedResetCarriesTheReason(t *testing.T) {
	v := Run(minimalConfig(), detect.System{}, Deps{
		Reset:    &fakeResetter{err: errResetBoom},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: newFakeLoadHandle()},
		Applier:  &fakeApplier{},
	}, Options{})

	if v.Status != "aborted" {
		t.Fatalf("status = %q, want aborted", v.Status)
	}
	if v.Error == "" {
		t.Fatal("aborted verdict carries no error; \"reset: failed\" alone is not a reason")
	}
	if !strings.Contains(v.Error, "boom") {
		t.Errorf("error = %q, want it to carry the reset failure's own text", v.Error)
	}
}

// spec: R-VER-16
//
// A compose reset prints a long progress log and then the reason. Relaying
// the raw tail joined by "; " buried the cause in a wall of "Container X
// Created" noise, on the one line a user reads first. Prefer the lines that
// actually indicate failure.
func TestShellResetter_SurfacesTheCauseNotTheProgressLog(t *testing.T) {
	script := `
echo " Container demo-db-1  Creating"
echo " Container demo-db-1  Created"
echo " Container demo-web-1  Creating"
echo " Container demo-web-1  Created"
echo " Container demo-db-1  Starting"
echo "Error response from daemon: invalid mount config: bind source path does not exist: db/password.txt" >&2
exit 1`
	err := ShellResetter{}.Reset(script)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "db/password.txt") {
		t.Fatalf("cause missing from %q", msg)
	}
	if strings.Contains(msg, "Created") {
		t.Errorf("progress noise relayed alongside the cause:\n%s", msg)
	}
}
