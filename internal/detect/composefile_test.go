package detect_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/detect"
)

// spec: R-DET-15
//
// The Compose Specification's precedence. Of the 40 examples in Docker's own
// docker/awesome-compose, 37 use compose.yaml, 2 use compose.yml and none use
// docker-compose.yml — which was the only name this tool looked for, so
// `init` failed before detection even ran.
func TestResolveComposeFile_Precedence(t *testing.T) {
	all := []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}
	for i, want := range all {
		// Write `want` and every lower-precedence name; `want` must win.
		dir := t.TempDir()
		for _, n := range all[i:] {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("services: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := detect.ResolveComposeFile(dir)
		if err != nil {
			t.Fatalf("ResolveComposeFile: %v", err)
		}
		if filepath.Base(got) != want {
			t.Errorf("with %v present, resolved %q, want %q", all[i:], filepath.Base(got), want)
		}
	}
}

// spec: R-DET-15
//
// With none present the error must name every filename tried, so the user
// knows what to create rather than guessing from one missing path.
func TestResolveComposeFile_ErrorNamesEveryCandidate(t *testing.T) {
	_, err := detect.ResolveComposeFile(t.TempDir())
	if err == nil {
		t.Fatal("want an error when no compose file exists")
	}
	for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		if !strings.Contains(err.Error(), n) {
			t.Errorf("error does not name %q: %v", n, err)
		}
	}
}
