package detect_test

import (
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-DET-14
func TestClientLibraryFromPackageJSONIsRecordedOnDependency(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)
	writeFile(t, dir, "package.json", `
{
  "name": "example-api",
  "dependencies": {
    "pg": "^8.11.0",
    "express": "^4.18.0"
  }
}
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Lang != "node" {
		t.Errorf("Lang = %q, want node", sys.Lang)
	}
	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	found := false
	for _, c := range sys.Deps[0].Clients {
		if c == "pg" {
			found = true
		}
	}
	if !found {
		t.Errorf("db.Clients = %v, want it to contain pg", sys.Deps[0].Clients)
	}
}

// spec: R-DET-14
func TestClientLibraryFromPyprojectTomlIsRecordedOnDependency(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)
	writeFile(t, dir, "pyproject.toml", `
[tool.poetry.dependencies]
python = "^3.11"
psycopg2 = "^2.9.9"
fastapi = "^0.110.0"
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Lang != "python" {
		t.Errorf("Lang = %q, want python", sys.Lang)
	}
	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	found := false
	for _, c := range sys.Deps[0].Clients {
		if c == "psycopg2" {
			found = true
		}
	}
	if !found {
		t.Errorf("db.Clients = %v, want it to contain psycopg2", sys.Deps[0].Clients)
	}
}

// spec: R-DET-14
func TestClientLibraryFromPep621PyprojectIsRecordedOnDependency(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)
	// PEP 621: the standardised, tool-agnostic dependency declaration used
	// by default by Hatch and PDM, and supported by Poetry too. R-DET-14
	// says "support pyproject.toml" at the ecosystem level, not one dialect
	// of it — a PEP 621 project must not be silently under-detected just
	// because it isn't using the Poetry table style.
	writeFile(t, dir, "pyproject.toml", `
[project]
name = "example-api"
dependencies = [
    "psycopg2>=2.9.9",
    "fastapi>=0.110.0",
]
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Lang != "python" {
		t.Errorf("Lang = %q, want python", sys.Lang)
	}
	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	found := false
	for _, c := range sys.Deps[0].Clients {
		if c == "psycopg2" {
			found = true
		}
	}
	if !found {
		t.Errorf("db.Clients = %v, want it to contain psycopg2 (PEP 621 array syntax)", sys.Deps[0].Clients)
	}
}

// spec: R-DET-14
func TestUnsupportedManifestPresentIsReportedAsGap(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
`)
	writeFile(t, dir, "Gemfile", `
source "https://rubygems.org"
gem "pg"
`)
	writeFile(t, dir, "pom.xml", `<project></project>`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	wantSubstrings := []string{"Gemfile", "pom.xml"}
	for _, want := range wantSubstrings {
		found := false
		for _, g := range sys.Gaps {
			if contains(g, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("gaps %v do not mention unsupported manifest %q", sys.Gaps, want)
		}
	}
}
