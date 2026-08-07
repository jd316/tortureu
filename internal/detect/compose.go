// Package detect turns a repo into a System description.
//
// D-3 caps detection at docker-compose + lockfile. Anything it cannot classify
// is reported as a gap, never guessed.
package detect

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Detect reads a compose file and describes the system.
func Detect(composePath string) (*System, error) {
	raw, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}

	var file struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, err
	}

	sys := &System{}
	for name, svc := range file.Services {
		// A service we build is the SUT; a service we pull is a dependency.
		if svc.Image == "" {
			continue
		}
		t := depType(svc.Image)
		if t == "" {
			sys.Gaps = append(sys.Gaps, "unrecognized image "+svc.Image+" (service "+name+") — classify it in torture.yaml")
			continue
		}
		sys.Deps = append(sys.Deps, Dep{Name: name, Type: t})
	}
	return sys, nil
}

// depType normalizes an image reference to a dependency type.
func depType(image string) string {
	if strings.HasPrefix(image, "postgres") {
		return "postgresql"
	}
	return ""
}
