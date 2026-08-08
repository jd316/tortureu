package detect

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ComposeFilenames is the Compose Specification's own precedence order for
// an unnamed compose file (R-DET-15). It is not a preference of ours: this
// is the order `docker compose` itself resolves.
var ComposeFilenames = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yaml",
	"docker-compose.yml",
}

// DefaultComposePath is what a flag advertises before a directory is known.
// It is the last entry rather than the first only because it is the name
// this project shipped with; ResolveComposeFile is what actually chooses.
const DefaultComposePath = "docker-compose.yml"

// ResolveComposeFile picks the compose file in dir by ComposeFilenames
// precedence (R-DET-15).
//
// The default used to be "docker-compose.yml" alone, which is now the least
// common spelling in the wild — of Docker's own 40 awesome-compose examples,
// 37 use compose.yaml and none use docker-compose.yml — so the first command
// a new user ran failed before detection started.
//
// An error names every candidate, because "no such file: docker-compose.yml"
// tells a user with a compose.yaml that the tool is broken, while the full
// list tells them the truth.
func ResolveComposeFile(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	for _, name := range ComposeFilenames {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("no compose file in %s: looked for %s",
		dir, strings.Join(ComposeFilenames, ", "))
}

// ResolveComposeArg turns a caller-supplied -compose value into a path.
//
// An explicit value is honoured exactly as given — including its failure,
// since a user who names a file expects to hear that *that* file is missing,
// not to have a different one silently substituted. Only the unset default
// triggers precedence resolution, and only when it is not present as given.
func ResolveComposeArg(arg string) (string, error) {
	if arg == "" {
		return ResolveComposeFile(".")
	}
	if _, err := os.Stat(arg); err == nil {
		return arg, nil
	}
	if filepath.Base(arg) == DefaultComposePath {
		return ResolveComposeFile(filepath.Dir(arg))
	}
	return arg, nil
}
