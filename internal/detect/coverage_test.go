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
	if sys.Coverage.AWS != detect.FactTrue {
		t.Errorf("Coverage.AWS = %v, want true (aws-sdk-go-v2 in go.mod)", sys.Coverage.AWS)
	}
	if sys.Coverage.Azure != detect.FactFalse {
		t.Errorf("Coverage.Azure = %v, want false (no azure SDK present, but go.mod was checked)", sys.Coverage.Azure)
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
	if sys.Coverage.Azure != detect.FactTrue {
		t.Errorf("Coverage.Azure = %v, want true (azure-sdk-for-go in go.mod)", sys.Coverage.Azure)
	}
	if sys.Coverage.AWS != detect.FactFalse {
		t.Errorf("Coverage.AWS = %v, want false (no aws SDK present, but go.mod was checked)", sys.Coverage.AWS)
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
	if sys.Coverage.LacksOtel != detect.FactTrue {
		t.Errorf("Coverage.LacksOtel = %v, want true (no otel client or collector anywhere)", sys.Coverage.LacksOtel)
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
	if sys.Coverage.LacksOtel != detect.FactFalse {
		t.Errorf("Coverage.LacksOtel = %v, want false (go.opentelemetry.io/otel is in go.mod)", sys.Coverage.LacksOtel)
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
	if sys.Coverage.LacksOtel != detect.FactFalse {
		t.Errorf("Coverage.LacksOtel = %v, want false (otel collector present in compose)", sys.Coverage.LacksOtel)
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
	if sys.Coverage.AWS != detect.FactFalse {
		t.Errorf("Coverage.AWS = %v, want false (no manifest at all — genuinely nothing to find)", sys.Coverage.AWS)
	}
	if sys.Coverage.Azure != detect.FactFalse {
		t.Errorf("Coverage.Azure = %v, want false (no manifest at all — genuinely nothing to find)", sys.Coverage.Azure)
	}
}

// spec: R-COV-6
// A predicate the system genuinely cannot evaluate MUST be reported as
// unevaluable, never silently treated as false. When the only manifest
// present is one R-DET-14 doesn't parse (Gemfile, pom.xml), AWS/Azure/
// lacks:otel are undetermined, not verified-false — a false default on
// lacks:otel in particular would have TortureU suggest OTel setup to a
// team whose (unparsed) Gemfile already has an OTel client.
func TestUnsupportedManifestOnlyReportsFactsAsUndeterminedNotFalse(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)
	writeFile(t, dir, "Gemfile", `
source "https://rubygems.org"
gem "aws-sdk-s3"
gem "opentelemetry-sdk"
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Coverage.AWS != detect.FactUnknown {
		t.Errorf("Coverage.AWS = %v, want unknown (only manifest is an unsupported Gemfile)", sys.Coverage.AWS)
	}
	if sys.Coverage.Azure != detect.FactUnknown {
		t.Errorf("Coverage.Azure = %v, want unknown (only manifest is an unsupported Gemfile)", sys.Coverage.Azure)
	}
	if sys.Coverage.LacksOtel != detect.FactUnknown {
		t.Errorf("Coverage.LacksOtel = %v, want unknown — a false default would wrongly suggest OTel setup to a team that already has it", sys.Coverage.LacksOtel)
	}
}

// spec: R-COV-6
func TestUnsupportedPomXMLManifestOnlyReportsFactsAsUndeterminedNotFalse(t *testing.T) {
	dir := t.TempDir()
	compose := plainCompose(t, dir)
	writeFile(t, dir, "pom.xml", `<project></project>`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Coverage.AWS != detect.FactUnknown {
		t.Errorf("Coverage.AWS = %v, want unknown (only manifest is an unsupported pom.xml)", sys.Coverage.AWS)
	}
	if sys.Coverage.Azure != detect.FactUnknown {
		t.Errorf("Coverage.Azure = %v, want unknown (only manifest is an unsupported pom.xml)", sys.Coverage.Azure)
	}
	if sys.Coverage.LacksOtel != detect.FactUnknown {
		t.Errorf("Coverage.LacksOtel = %v, want unknown (only manifest is an unsupported pom.xml)", sys.Coverage.LacksOtel)
	}
}
