package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jd316/tortureu/internal/detect"
)

// gitRepoWithCommit builds a throwaway git repo with one commit and returns
// its directory and the full 40-character hash.
func gitRepoWithCommit(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; R-VER-12 resolves the anchor through git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "base")

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return dir, string(out[:40])
}

// spec: R-VER-12
//
// The anchor must be the full 40-character hash. VERDICT.md §4 abbreviates
// it for display, but the JSON document is what a trend store reads, and
// `bencher run --hash` rejects anything shorter outright — so abbreviating
// at the source would break the one consumer that needs it.
func TestCommitAnchor_IsTheFullHash(t *testing.T) {
	dir, want := gitRepoWithCommit(t)

	got := commitAnchor(filepath.Join(dir, "docker-compose.yml"))
	if got != want {
		t.Fatalf("commitAnchor = %q, want the full HEAD hash %q", got, want)
	}
	if len(got) != 40 {
		t.Fatalf("anchor is %d chars, want 40", len(got))
	}
}

// spec: R-VER-12
//
// Outside a git checkout the field stays empty. A placeholder would be
// worse than nothing: a trend keyed on a fabricated commit silently
// compares runs that are not what it claims they are.
func TestCommitAnchor_EmptyOutsideAGitRepo(t *testing.T) {
	dir := t.TempDir()
	if got := commitAnchor(filepath.Join(dir, "docker-compose.yml")); got != "" {
		t.Fatalf("commitAnchor = %q, want empty outside a git repo", got)
	}
}

// spec: R-VER-12
//
// And it must actually reach the verdict document — the failure this
// requirement exists for was a field no producer ever wrote.
func TestRun_VerdictCarriesTheCommitAnchor(t *testing.T) {
	dir, want := gitRepoWithCommit(t)

	handle := newFakeLoadHandle()
	handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	close(handle.markers)

	cfg := minimalConfig()
	cfg.Target.Compose = filepath.Join(dir, "docker-compose.yml")

	v := Run(cfg, detect.System{}, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  &fakeApplier{},
	}, Options{})

	if v.Commit != want {
		t.Fatalf("verdict commit = %q, want %q", v.Commit, want)
	}
}
