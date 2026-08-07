package detect_test

import (
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

func plainCompose(t *testing.T, dir string) string {
	t.Helper()
	return writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)
}

// spec: R-COV-5
func TestSpecOpenapiFactTrueWhenOpenapiDocumentExists(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)
	writeFile(t, dir, "openapi.yaml", `openapi: 3.0.0`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sys.Coverage.OpenAPI {
		t.Errorf("Coverage.OpenAPI = false, want true (openapi.yaml present)")
	}
}

// spec: R-COV-5
func TestSpecProtoFactTrueWhenProtoFileExists(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)
	// Nested under a conventional proto/ directory, not the repo root —
	// the check must not be limited to a flat top-level listing.
	writeFile(t, dir, "proto/service.proto", `syntax = "proto3";`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sys.Coverage.Proto {
		t.Errorf("Coverage.Proto = false, want true (proto/service.proto present)")
	}
}

// spec: R-COV-5
func TestPlatformK8sFactTrueWhenHelmChartExists(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)
	writeFile(t, dir, "charts/myapp/Chart.yaml", `apiVersion: v2
name: myapp
version: 0.1.0
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sys.Coverage.K8s {
		t.Errorf("Coverage.K8s = false, want true (Chart.yaml present)")
	}
}

// spec: R-COV-5
func TestPlatformAWSFactTrueWhenAWSSDKInManifest(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)
	writeFile(t, dir, "go.mod", `
module example.com/api

go 1.22

require github.com/aws/aws-sdk-go-v2/service/s3 v1.34.0
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sys.Coverage.AWS {
		t.Errorf("Coverage.AWS = false, want true (aws-sdk-go-v2 in go.mod)")
	}
	if sys.Coverage.Azure {
		t.Errorf("Coverage.Azure = true, want false (no azure SDK present)")
	}
}

// spec: R-COV-5
func TestPlatformAzureFactTrueWhenAzureSDKInManifest(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)
	writeFile(t, dir, "go.mod", `
module example.com/api

go 1.22

require github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.3.0
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sys.Coverage.Azure {
		t.Errorf("Coverage.Azure = false, want true (azure-sdk-for-go in go.mod)")
	}
	if sys.Coverage.AWS {
		t.Errorf("Coverage.AWS = true, want false (no aws SDK present)")
	}
}

// spec: R-COV-5
// lacks:otel is a negative predicate: a detector that always reports
// "absent" would wrongly suggest OTel setup to a system that already has
// it, so both the true and false cases are tested explicitly.
func TestLacksOtelFactTrueWhenNoOtelClientOrCollectorPresent(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sys.Coverage.LacksOtel {
		t.Errorf("Coverage.LacksOtel = false, want true (no otel client or collector anywhere)")
	}
}

// spec: R-COV-5
func TestLacksOtelFactFalseWhenOtelClientInManifest(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)
	writeFile(t, dir, "go.mod", `
module example.com/api

go 1.22

require go.opentelemetry.io/otel v1.24.0
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Coverage.LacksOtel {
		t.Errorf("Coverage.LacksOtel = true, want false (go.opentelemetry.io/otel is in go.mod)")
	}
}

// spec: R-COV-5
func TestLacksOtelFactFalseWhenOtelCollectorInCompose(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
  otelcol:
    image: otel/opentelemetry-collector:0.96.0
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Coverage.LacksOtel {
		t.Errorf("Coverage.LacksOtel = true, want false (otel collector present in compose)")
	}
}

// spec: R-COV-5
func TestCoverageFactsAllFalseForPlainRepoWithNoIndicators(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Coverage.OpenAPI {
		t.Errorf("Coverage.OpenAPI = true, want false")
	}
	if sys.Coverage.Proto {
		t.Errorf("Coverage.Proto = true, want false")
	}
	if sys.Coverage.K8s {
		t.Errorf("Coverage.K8s = true, want false")
	}
	if sys.Coverage.AWS {
		t.Errorf("Coverage.AWS = true, want false")
	}
	if sys.Coverage.Azure {
		t.Errorf("Coverage.Azure = true, want false")
	}
}
