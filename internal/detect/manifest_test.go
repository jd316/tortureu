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

// railsGemfile is a realistic Rails 7 Gemfile: groups, a platforms block,
// comments (including a commented-out gem, which MUST NOT be read as a
// declaration), and version constraints in both the `gem "x", "~> 1.0"`
// and keyword forms.
const railsGemfile = `source "https://rubygems.org"
git_source(:github) { |repo| "https://github.com/#{repo}.git" }

ruby "3.2.2"

gem "rails", "~> 7.1.3"
gem "pg", "~> 1.5"
gem "puma", ">= 5.0"
gem "redis", ">= 4.0.1"
gem "bunny", "~> 2.22"
gem "aws-sdk-s3", require: false
gem "aws-sdk-sqs", require: false
# gem "mysql2", "~> 0.5"   # migrated off MySQL in 2023
gem "bootsnap", require: false

group :development, :test do
  gem "debug", platforms: %i[ mri mingw x64_mingw ]
  gem "rspec-rails", "~> 6.1"
end
`

// spec: R-DET-14
func TestClientLibraryFromGemfileIsRecordedOnDependency(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
  cache:
    image: redis:7
`)
	writeFile(t, dir, "Gemfile", railsGemfile)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Lang != "ruby" {
		t.Errorf("Lang = %q, want ruby", sys.Lang)
	}
	wantClients := map[string]string{"postgresql": "pg", "redis": "redis"}
	for typ, want := range wantClients {
		if !depHasClient(sys, typ, want) {
			t.Errorf("dep %s has no client %q: %+v", typ, want, sys.Deps)
		}
	}
	// A commented-out gem is not a declaration: reading it would invent a
	// MySQL dependency this repo migrated off (R-DET-3, no guessing).
	for _, d := range sys.Deps {
		if d.Type == "mysql" {
			t.Errorf("mysql dependency invented from a commented-out gem: %+v", d)
		}
	}
	// sqs is a lockfile-only type (SPEC §3.1) with no compose service —
	// R-DET-13 says record it anyway. s3 is image-or-lockfile sourced, so
	// per R-DET-10 it is NOT fabricated from the gem alone.
	if !depHasClient(sys, "sqs", "aws-sdk-sqs") {
		t.Errorf("sqs client aws-sdk-sqs not recorded: %+v", sys.Deps)
	}
	for _, d := range sys.Deps {
		if d.Type == "s3" {
			t.Errorf("s3 dependency fabricated from a gem despite being image-sourced (R-DET-10): %+v", d)
		}
	}
	if sys.Coverage.AWS != detect.FactTrue {
		t.Errorf("Coverage.AWS = %v, want true (aws-sdk-s3 in the Gemfile)", sys.Coverage.AWS)
	}
	for _, g := range sys.Gaps {
		if contains(g, "Gemfile") {
			t.Errorf("Gemfile still reported as an unsupported manifest: %q", g)
		}
	}
}

// spec: R-DET-14
// A Gemfile.lock's DEPENDENCIES section lists the same direct dependencies
// the Gemfile does, so it is the fallback when only the lockfile is
// present. The resolved `GEM specs:` closure above it MUST NOT be read:
// activerecord pulling in a gem is not evidence this service talks to it.
func TestClientLibraryFromGemfileLockReadsDirectDependenciesOnly(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)
	writeFile(t, dir, "Gemfile.lock", `GEM
  remote: https://rubygems.org/
  specs:
    activesupport (7.1.3)
      concurrent-ruby (~> 1.0, >= 1.0.2)
    mysql2 (0.5.5)
    pg (1.5.4)
    rails (7.1.3)

PLATFORMS
  ruby

DEPENDENCIES
  pg (~> 1.5)
  rails (~> 7.1.3)

RUBY VERSION
   ruby 3.2.2p53

BUNDLED WITH
   2.4.19
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Lang != "ruby" {
		t.Errorf("Lang = %q, want ruby", sys.Lang)
	}
	if !depHasClient(sys, "postgresql", "pg") {
		t.Errorf("postgresql has no client pg: %+v", sys.Deps)
	}
	for _, d := range sys.Deps {
		if d.Type == "mysql" {
			t.Errorf("mysql dependency invented from the resolved-specs closure: %+v", d)
		}
	}
}

// springBootPom is a realistic Spring Boot 3 pom.xml: a parent, property
// interpolation, starters, a runtime-scoped JDBC driver, a test-scoped
// dependency, and a dependencyManagement block whose entries are NOT this
// module's dependencies.
const springBootPom = `<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0"
         xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
         xsi:schemaLocation="http://maven.apache.org/POM/4.0.0 https://maven.apache.org/xsd/maven-4.0.0.xsd">
  <modelVersion>4.0.0</modelVersion>
  <parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>3.2.4</version>
    <relativePath/>
  </parent>
  <groupId>com.example</groupId>
  <artifactId>checkout-api</artifactId>
  <version>0.0.1-SNAPSHOT</version>

  <properties>
    <java.version>21</java.version>
    <awssdk.version>2.25.11</awssdk.version>
  </properties>

  <dependencyManagement>
    <dependencies>
      <dependency>
        <groupId>com.datastax.oss</groupId>
        <artifactId>java-driver-core</artifactId>
        <version>4.17.0</version>
      </dependency>
    </dependencies>
  </dependencyManagement>

  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-web</artifactId>
    </dependency>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-data-jpa</artifactId>
    </dependency>
    <dependency>
      <groupId>org.postgresql</groupId>
      <artifactId>postgresql</artifactId>
      <scope>runtime</scope>
    </dependency>
    <dependency>
      <groupId>redis.clients</groupId>
      <artifactId>jedis</artifactId>
      <version>5.1.2</version>
    </dependency>
    <dependency>
      <groupId>org.apache.kafka</groupId>
      <artifactId>kafka-clients</artifactId>
    </dependency>
    <dependency>
      <groupId>software.amazon.awssdk</groupId>
      <artifactId>sqs</artifactId>
      <version>${awssdk.version}</version>
    </dependency>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-test</artifactId>
      <scope>test</scope>
    </dependency>
  </dependencies>
</project>
`

// spec: R-DET-14
func TestClientLibraryFromPomXMLIsRecordedOnDependency(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
  cache:
    image: redis:7
  broker:
    image: confluentinc/cp-kafka:7.6.0
`)
	writeFile(t, dir, "pom.xml", springBootPom)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Lang != "java" {
		t.Errorf("Lang = %q, want java", sys.Lang)
	}
	wantClients := map[string]string{
		"postgresql": "org.postgresql:postgresql",
		"redis":      "redis.clients:jedis",
		"kafka":      "org.apache.kafka:kafka-clients",
		"sqs":        "software.amazon.awssdk:sqs",
	}
	for typ, want := range wantClients {
		if !depHasClient(sys, typ, want) {
			t.Errorf("dep %s has no client %q: %+v", typ, want, sys.Deps)
		}
	}
	// dependencyManagement pins a version for a module that may never use
	// the library; treating it as a dependency would invent a Cassandra
	// the SUT does not talk to.
	for _, d := range sys.Deps {
		if d.Type == "cassandra" {
			t.Errorf("cassandra invented from a dependencyManagement pin: %+v", d)
		}
	}
	if sys.Coverage.AWS != detect.FactTrue {
		t.Errorf("Coverage.AWS = %v, want true (software.amazon.awssdk in the pom)", sys.Coverage.AWS)
	}
	for _, g := range sys.Gaps {
		if contains(g, "pom.xml") {
			t.Errorf("pom.xml still reported as an unsupported manifest: %q", g)
		}
	}
}

// spec: R-DET-14
// An aggregator pom's real dependencies live in module poms outside any
// compose-declared directory (R-DET-1), so reading the aggregator alone
// MUST name the unread modules as a gap rather than report an empty
// dependency list as fact.
func TestMavenAggregatorPomReportsUnreadModulesAsGap(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
`)
	writeFile(t, dir, "pom.xml", `<?xml version="1.0" encoding="UTF-8"?>
<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>platform</artifactId>
  <packaging>pom</packaging>
  <modules>
    <module>checkout-api</module>
    <module>ledger-worker</module>
  </modules>
</project>
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	found := false
	for _, g := range sys.Gaps {
		if contains(g, "pom.xml") && contains(g, "checkout-api") {
			found = true
		}
	}
	if !found {
		t.Errorf("Gaps = %v, want one naming the unread aggregator modules", sys.Gaps)
	}
}

// depHasClient reports whether sys has a dependency of type typ carrying
// client, both in Clients and in the per-service ClientRefs (R-DET-5).
func depHasClient(sys *detect.System, typ, client string) bool {
	for _, d := range sys.Deps {
		if d.Type != typ {
			continue
		}
		for _, c := range d.Clients {
			if c == client {
				return true
			}
		}
	}
	return false
}
