package run

import "testing"

// spec: R-CFG-21
func TestShellResetter_RunsCommandAndReturnsError(t *testing.T) {
	if err := (ShellResetter{}).Reset("true"); err != nil {
		t.Errorf("Reset(\"true\") = %v, want nil", err)
	}
	if err := (ShellResetter{}).Reset("false"); err == nil {
		t.Error("Reset(\"false\") = nil, want an error — a failing reset command must be reported so the caller aborts (R-EXE-2)")
	}
}

// spec: R-CFG-21
func TestShellResetter_RunsCompoundShellCommand(t *testing.T) {
	// R-CFG-21's own default reset command is a compound shell command
	// ("docker compose down -v && docker compose up -d --wait"); Reset must
	// interpret it via a shell, not exec it as a single literal argv.
	if err := (ShellResetter{}).Reset("true && true"); err != nil {
		t.Errorf("Reset(\"true && true\") = %v, want nil", err)
	}
}
