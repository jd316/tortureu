package detect

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// skipDirs are directories never worth descending into for a file-presence
// check: they are large, third-party, or version control, and cannot
// themselves contain a repo's own OpenAPI/proto/Helm artefacts.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".venv":        true,
	"venv":         true,
	"dist":         true,
	"build":        true,
}

// openapiFilenames are the conventional names for an OpenAPI/Swagger
// document (R-COV-5 spec:openapi).
var openapiFilenames = map[string]bool{
	"openapi.yaml": true,
	"openapi.yml":  true,
	"openapi.json": true,
	"swagger.yaml": true,
	"swagger.yml":  true,
	"swagger.json": true,
}

// k8sFilenames are conventional Kubernetes/Helm indicator files (R-COV-5
// platform:k8s): a Helm chart manifest or a Kustomize overlay.
var k8sFilenames = map[string]bool{
	"Chart.yaml":         true,
	"kustomization.yaml": true,
	"kustomization.yml":  true,
}

// k8sDirNames are conventional directory names holding raw Kubernetes
// manifests (R-COV-5 platform:k8s).
var k8sDirNames = map[string]bool{
	"k8s":        true,
	"kubernetes": true,
}

// detectFileCoverage walks dir (skipping vendored/VCS directories) once,
// setting the R-COV-5 facts that are pure file-presence checks:
// spec:openapi, spec:proto, platform:k8s. This is presence detection only —
// no file content is read — staying inside R-DET-1's bound.
func detectFileCoverage(dir string, sys *System) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			if k8sDirNames[name] {
				sys.Coverage.K8s = true
			}
			return nil
		}
		if openapiFilenames[name] {
			sys.Coverage.OpenAPI = true
		}
		if strings.HasSuffix(name, ".proto") {
			sys.Coverage.Proto = true
		}
		if k8sFilenames[name] {
			sys.Coverage.K8s = true
		}
		return nil
	})
}
