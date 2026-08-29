package detect_test

import (
	"testing"

	"github.com/jd316/tortureu/internal/detect"
)

// spec: R-DET-5
func TestClientLibraryFromGoModIsRecordedOnDependency(t *testing.T) {
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
	found := false
	for _, c := range sys.Deps[0].Clients {
		if c == "github.com/lib/pq" {
			found = true
		}
	}
	if !found {
		t.Errorf("db.Clients = %v, want it to contain github.com/lib/pq", sys.Deps[0].Clients)
	}
}

// spec: R-DET-10
func TestLockfileOnlyTypeIsNotInferredFromImageAndIsFoundViaLockfile(t *testing.T) {
	dir := t.TempDir()
	// No compose service produces an "sqs" or "zookeeper" image match; sqs
	// has no compose representation at all (it's an AWS-hosted service).
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
`)
	writeFile(t, dir, "go.mod", `
module example.com/api

go 1.22

require (
	github.com/aws/aws-sdk-go-v2/service/sqs v1.34.0
	github.com/example/zookeeper-fake-client v0.0.1
)
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	var sqsDep *detect.Dep
	for i := range sys.Deps {
		if sys.Deps[i].Type == "sqs" {
			sqsDep = &sys.Deps[i]
		}
		// zookeeper's only recognized source is `image` (R-DET-9); a lockfile
		// entry that merely mentions "zookeeper" MUST NOT manufacture a dep.
		if sys.Deps[i].Type == "zookeeper" {
			t.Errorf("zookeeper dep manufactured from lockfile, but its source is image-only: %+v", sys.Deps[i])
		}
	}
	if sqsDep == nil {
		t.Fatalf("no sqs dependency found via lockfile; deps: %+v", sys.Deps)
	}
	found := false
	for _, c := range sqsDep.Clients {
		if c == "github.com/aws/aws-sdk-go-v2/service/sqs" {
			found = true
		}
	}
	if !found {
		t.Errorf("sqs dep clients = %v, want the sqs sdk import", sqsDep.Clients)
	}
}

// spec: R-DET-13
// A managed service like SQS has no container, so it can never get an
// address the way an in-compose dependency does — it MUST still be
// recorded (with clients, but no address) because it is still a
// dependency that can fail. This is a verification test: the behaviour
// already exists (built to satisfy R-DET-10's TestLockfileOnlyType... in
// an earlier round), but nothing previously cited R-DET-13 or asserted
// Address == "" specifically.
func TestManagedServiceDependencyIsRecordedWithClientsButNoAddress(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
`)
	writeFile(t, dir, "go.mod", `
module example.com/api

go 1.22

require github.com/aws/aws-sdk-go-v2/service/dynamodb v1.34.0
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	var ddbDep *detect.Dep
	for i := range sys.Deps {
		if sys.Deps[i].Type == "dynamodb" {
			ddbDep = &sys.Deps[i]
		}
	}
	if ddbDep == nil {
		t.Fatalf("no dynamodb dependency found via lockfile; deps: %+v", sys.Deps)
	}
	if len(ddbDep.Clients) == 0 {
		t.Errorf("dynamodb dep has no clients, want the aws-sdk-go-v2 import recorded")
	}
	if ddbDep.Address != "" {
		t.Errorf("dynamodb dep Address = %q, want empty — a managed service has no container, hence no address (R-DET-13)", ddbDep.Address)
	}
}

// spec: R-DET-1
func TestGeneralSourceFilesAreNotScannedOnlyManifests(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)
	// A stray source file mentioning a client library, but no go.mod/lockfile
	// present. R-DET-1 forbids general source analysis: this MUST be ignored.
	writeFile(t, dir, "main.go", `
package main

import _ "github.com/lib/pq"

func main() {}
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Lang != "" {
		t.Errorf("Lang = %q, want empty (no manifest present)", sys.Lang)
	}
	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	if len(sys.Deps[0].Clients) != 0 {
		t.Errorf("Clients = %v, want empty — general .go source must not be scanned", sys.Deps[0].Clients)
	}
}
