package detect_test

import (
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-DET-1
//
// The common monorepo layout: docker-compose.yml at the root, each
// service's own manifest under its build context, nothing at the project
// root at all. Before this fix, detection only ever read the compose
// project root, so a repo laid out this way — which an E1 eval found is
// the common one — yielded no detected clients whatsoever: the verdict's
// candidate config surface (D-9) silently produced nothing for exactly the
// repos most likely to need it.
func TestClientLibraryFromServiceBuildContextIsAttributedToThatService(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: ./api
  db:
    image: postgres:16
`)
	writeFile(t, dir, "api/go.mod", `
module example.com/api

go 1.22

require github.com/lib/pq v1.10.9
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	dep := sys.Deps[0]

	found := false
	for _, c := range dep.Clients {
		if c == "github.com/lib/pq" {
			found = true
		}
	}
	if !found {
		t.Fatalf("db.Clients = %v, want it to contain github.com/lib/pq (found under api/'s build context)", dep.Clients)
	}

	attributed := false
	for _, ref := range dep.ClientRefs {
		if ref.Import == "github.com/lib/pq" && ref.Service == "api" {
			attributed = true
		}
	}
	if !attributed {
		t.Errorf("db.ClientRefs = %+v, want github.com/lib/pq attributed to service %q", dep.ClientRefs, "api")
	}
}

// spec: R-DET-1
// Two services, each with their own build context and manifest, talking
// to different dependencies — proves attribution is per-service, not
// pooled globally: worker's client must not attach to db (postgres) and
// vice versa, and each ends up on the right dependency with the right
// service tag.
func TestClientLibrariesFromMultipleServiceBuildContextsAreAttributedSeparately(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: ./api
  worker:
    build: ./worker
  db:
    image: postgres:16
  cache:
    image: redis:7
`)
	writeFile(t, dir, "api/go.mod", `
module example.com/api

go 1.22

require github.com/lib/pq v1.10.9
`)
	writeFile(t, dir, "worker/go.mod", `
module example.com/worker

go 1.22

require github.com/redis/go-redis/v9 v9.5.1
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	var dbDep, cacheDep *detect.Dep
	for i := range sys.Deps {
		switch sys.Deps[i].Name {
		case "db":
			dbDep = &sys.Deps[i]
		case "cache":
			cacheDep = &sys.Deps[i]
		}
	}
	if dbDep == nil || cacheDep == nil {
		t.Fatalf("deps = %+v, want both db and cache", sys.Deps)
	}

	if len(dbDep.ClientRefs) != 1 || dbDep.ClientRefs[0].Service != "api" {
		t.Errorf("db.ClientRefs = %+v, want exactly one ref attributed to api", dbDep.ClientRefs)
	}
	if len(cacheDep.ClientRefs) != 1 || cacheDep.ClientRefs[0].Service != "worker" {
		t.Errorf("cache.ClientRefs = %+v, want exactly one ref attributed to worker", cacheDep.ClientRefs)
	}
}

// spec: R-DET-1
// The project-root case must still work unchanged after adding
// build-context scanning: a single-service repo with its manifest at the
// root, where that root also happens to be the service's own build
// context (`build: .`), attributes the client to that service — it is not
// left at "" just because the scan that found it was the root-level one.
func TestClientLibraryFromProjectRootIsAttributedToServiceBuiltThere(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)
	writeFile(t, dir, "go.mod", `
module example.com/api

go 1.22

require github.com/lib/pq v1.10.9
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Lang != "go" {
		t.Errorf("Lang = %q, want go", sys.Lang)
	}
	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	dep := sys.Deps[0]

	found := false
	for _, c := range dep.Clients {
		if c == "github.com/lib/pq" {
			found = true
		}
	}
	if !found {
		t.Fatalf("db.Clients = %v, want it to contain github.com/lib/pq", dep.Clients)
	}

	attributed := false
	for _, ref := range dep.ClientRefs {
		if ref.Import == "github.com/lib/pq" && ref.Service == "api" {
			attributed = true
		}
	}
	if !attributed {
		t.Errorf("db.ClientRefs = %+v, want github.com/lib/pq attributed to service %q (build: . is api's own context)", dep.ClientRefs, "api")
	}
}

// spec: R-DET-7
// A service's build context with no manifest at all is not itself a gap —
// a service may legitimately have none (e.g. a prebuilt static binary).
func TestServiceBuildContextWithNoManifestIsNotAGap(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: ./api
  db:
    image: postgres:16
`)
	writeFile(t, dir, "api/main.go", `package main

func main() {}
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(sys.Gaps) != 0 {
		t.Errorf("Gaps = %v, want none — a build context with no manifest is not a gap (R-DET-7)", sys.Gaps)
	}
}

// spec: R-DET-14
// A Gemfile in a service's own build context is read like any other
// manifest since TBD-7 closed, and its clients are attributed to that
// service (R-DET-5) — not left as a gap.
func TestGemfileInServiceBuildContextIsAttributedToThatService(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  worker:
    build: ./worker
  db:
    image: postgres:16
`)
	writeFile(t, dir, "worker/Gemfile", `
source "https://rubygems.org"
gem "pg", "~> 1.5"
gem "sidekiq"
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	for _, g := range sys.Gaps {
		if contains(g, "Gemfile") {
			t.Errorf("Gemfile in a build context still reported as a gap: %q", g)
		}
	}
	found := false
	for _, d := range sys.Deps {
		if d.Type != "postgresql" {
			continue
		}
		for _, ref := range d.ClientRefs {
			if ref.Import == "pg" && ref.Service == "worker" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("Deps = %+v, want pg attributed to worker", sys.Deps)
	}
}

// spec: R-DET-7
// A manifest in a service's build context whose dependencies we are not
// allowed to read (a Maven aggregator pom: its modules are outside every
// compose-declared directory) MUST still surface as a gap rather than
// silently vanish — the same rule R-DET-14 applies at the project root.
func TestUnreadableManifestInServiceBuildContextIsReportedAsGap(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  worker:
    build: ./worker
  db:
    image: postgres:16
`)
	writeFile(t, dir, "worker/pom.xml", `<project>
  <artifactId>worker-parent</artifactId>
  <packaging>pom</packaging>
  <modules>
    <module>ledger-worker</module>
  </modules>
</project>`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	found := false
	for _, g := range sys.Gaps {
		if contains(g, "worker") && contains(g, "pom.xml") {
			found = true
		}
	}
	if !found {
		t.Errorf("Gaps = %v, want one naming worker's unread aggregator pom", sys.Gaps)
	}
}
