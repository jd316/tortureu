package detect

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// clientPattern recognizes a client library import as belonging to a
// dependency type, per the client column of SPEC.md §3.1. There is one
// table per R-DET-14 ecosystem, since the same dependency is named
// differently in each ("pg", "psycopg2", "org.postgresql:postgresql").
type clientPattern struct {
	typ     string
	imports []string
}

// lockfileOnly marks the R-DET-9 types whose only source is `lockfile`.
// Per R-DET-13 these must be recorded even with no corresponding compose
// service. Types not in this set (e.g. zookeeper, cockroach) are image-only
// and per R-DET-10 MUST NOT be manufactured from a lockfile match.
var lockfileOnly = map[string]bool{
	"sqs":       true,
	"dynamodb":  true,
	"snowflake": true,
	"websocket": true,
	"jms":       true,
	"ldap":      true,
	"soap":      true,
}

var goClientPatterns = []clientPattern{
	{"postgresql", []string{"github.com/lib/pq", "github.com/jackc/pgx"}},
	{"mysql", []string{"github.com/go-sql-driver/mysql"}},
	{"redis", []string{"github.com/go-redis/redis", "github.com/redis/go-redis"}},
	{"mongodb", []string{"go.mongodb.org/mongo-driver"}},
	{"kafka", []string{"github.com/Shopify/sarama", "github.com/IBM/sarama"}},
	{"rabbitmq", []string{"github.com/rabbitmq/amqp091-go", "github.com/streadway/amqp"}},
	{"nats", []string{"github.com/nats-io/nats.go"}},
	{"cassandra", []string{"github.com/gocql/gocql"}},
	{"etcd", []string{"go.etcd.io/etcd/client"}},
	{"s3", []string{"github.com/aws/aws-sdk-go-v2/service/s3", "github.com/aws/aws-sdk-go/service/s3"}},
	{"sqs", []string{"github.com/aws/aws-sdk-go-v2/service/sqs", "github.com/aws/aws-sdk-go/service/sqs"}},
	{"dynamodb", []string{"github.com/aws/aws-sdk-go-v2/service/dynamodb", "github.com/aws/aws-sdk-go/service/dynamodb"}},
	{"mqtt", []string{"github.com/eclipse/paho.mqtt.golang"}},
	{"websocket", []string{"github.com/gorilla/websocket"}},
	{"ldap", []string{"github.com/go-ldap/ldap"}},
}

// nodeClientPatterns maps an npm package name to its R-DET-9 type.
var nodeClientPatterns = []clientPattern{
	{"postgresql", []string{"pg"}},
	{"mysql", []string{"mysql2"}},
	{"redis", []string{"ioredis"}},
	{"mongodb", []string{"mongoose"}},
	{"kafka", []string{"kafkajs"}},
	{"rabbitmq", []string{"amqplib"}},
	{"nats", []string{"nats"}},
	{"s3", []string{"@aws-sdk/client-s3"}},
	{"sqs", []string{"@aws-sdk/client-sqs"}},
	{"dynamodb", []string{"@aws-sdk/client-dynamodb"}},
	{"websocket", []string{"ws"}},
	{"smtp", []string{"nodemailer"}},
	{"ldap", []string{"ldapjs"}},
}

// pyClientPatterns maps a PyPI package name to its R-DET-9 type.
var pyClientPatterns = []clientPattern{
	{"postgresql", []string{"psycopg2", "psycopg", "asyncpg"}},
	{"mysql", []string{"pymysql"}},
	{"redis", []string{"redis"}},
	{"mongodb", []string{"pymongo"}},
	{"kafka", []string{"confluent-kafka", "kafka-python"}},
	{"rabbitmq", []string{"pika"}},
	{"mqtt", []string{"paho-mqtt"}},
	{"websocket", []string{"websockets"}},
	{"ldap", []string{"ldap3"}},
}

// rubyClientPatterns maps a RubyGems gem name to its R-DET-9 type. Names
// are matched by prefix, so "redis" also covers the redis-client gem the
// modern redis gem builds on, and "aws-sdk-s3" is distinct from
// "aws-sdk-sqs" — which is why the aws-sdk gems are listed per service.
var rubyClientPatterns = []clientPattern{
	{"postgresql", []string{"pg"}},
	{"mysql", []string{"mysql2"}},
	{"redis", []string{"redis"}},
	{"mongodb", []string{"mongo", "mongoid"}},
	{"kafka", []string{"ruby-kafka", "rdkafka", "karafka"}},
	{"rabbitmq", []string{"bunny"}},
	{"nats", []string{"nats-pure"}},
	{"elasticsearch", []string{"elasticsearch"}},
	{"cassandra", []string{"cassandra-driver"}},
	{"s3", []string{"aws-sdk-s3"}},
	{"sqs", []string{"aws-sdk-sqs"}},
	{"dynamodb", []string{"aws-sdk-dynamodb"}},
	{"mqtt", []string{"mqtt"}},
	{"websocket", []string{"faye-websocket"}},
	{"ldap", []string{"net-ldap"}},
	{"soap", []string{"savon"}},
}

// javaClientPatterns maps a Maven coordinate ("groupId:artifactId") to its
// R-DET-9 type. Coordinates rather than bare artifact ids: "sqs" and "s3"
// are artifact ids under software.amazon.awssdk, and matching those alone
// would fire on any dependency that happened to share the name.
var javaClientPatterns = []clientPattern{
	{"postgresql", []string{"org.postgresql:postgresql"}},
	{"mysql", []string{"mysql:mysql-connector-java", "com.mysql:mysql-connector-j"}},
	{"redis", []string{"redis.clients:jedis", "io.lettuce:lettuce-core", "org.springframework.data:spring-data-redis"}},
	{"mongodb", []string{"org.mongodb:mongodb-driver", "org.mongodb:mongo-java-driver"}},
	{"kafka", []string{"org.apache.kafka:kafka-clients", "org.springframework.kafka:spring-kafka"}},
	{"rabbitmq", []string{"com.rabbitmq:amqp-client", "org.springframework.amqp:spring-rabbit"}},
	{"elasticsearch", []string{"co.elastic.clients:elasticsearch-java", "org.elasticsearch.client:elasticsearch-rest"}},
	{"cassandra", []string{"com.datastax.oss:java-driver-core"}},
	{"etcd", []string{"io.etcd:jetcd-core"}},
	{"s3", []string{"software.amazon.awssdk:s3", "com.amazonaws:aws-java-sdk-s3"}},
	{"sqs", []string{"software.amazon.awssdk:sqs", "com.amazonaws:aws-java-sdk-sqs"}},
	{"dynamodb", []string{"software.amazon.awssdk:dynamodb", "com.amazonaws:aws-java-sdk-dynamodb"}},
	{"snowflake", []string{"net.snowflake:snowflake-jdbc"}},
	{"mqtt", []string{"org.eclipse.paho:org.eclipse.paho.client.mqttv3"}},
	{"jms", []string{"javax.jms:javax.jms-api", "jakarta.jms:jakarta.jms-api", "org.springframework:spring-jms"}},
	{"ldap", []string{"org.springframework.ldap:spring-ldap-core"}},
	{"soap", []string{"org.springframework.ws:spring-ws-core"}},
}

// awsSDKImports and azureSDKImports recognize a cloud provider SDK in a
// manifest, per R-COV-5's platform:aws / platform:azure fact. Keyed by the
// same sys.Lang values ("go", "node", "python") detectLockfiles sets.
var awsSDKImports = map[string][]string{
	"go":     {"github.com/aws/aws-sdk-go"},
	"node":   {"aws-sdk", "@aws-sdk/"},
	"python": {"boto3", "botocore"},
	"ruby":   {"aws-sdk"},
	"java":   {"software.amazon.awssdk:", "com.amazonaws:"},
}

var azureSDKImports = map[string][]string{
	"go":     {"github.com/Azure/azure-sdk-for-go"},
	"node":   {"@azure/"},
	"python": {"azure-"},
	"ruby":   {"azure-"},
	"java":   {"com.azure:"},
}

// otelClientImports recognizes an OpenTelemetry client in a manifest, per
// R-COV-5's lacks:otel fact (the other half is the compose-side collector
// check in compose.go's isOtelCollector).
var otelClientImports = map[string][]string{
	"go":     {"go.opentelemetry.io/otel"},
	"node":   {"@opentelemetry/"},
	"python": {"opentelemetry-"},
	"ruby":   {"opentelemetry-"},
	"java":   {"io.opentelemetry:"},
}

// goModRequireRe finds "<module path> vX.Y.Z" pairs, matching both the
// single-line and block forms of a require directive.
var goModRequireRe = regexp.MustCompile(`(\S+)\s+v\d+\.\d+\.\d+\S*`)

// pyprojectKeyRe finds "name = ..." assignments in a poetry-style
// [tool.poetry.dependencies] table.
var pyprojectKeyRe = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_.-]+)\s*=`)

// pep621DepsArrayRe finds the contents of any "dependencies = [...]" array —
// PEP 621's standardized, tool-agnostic dependency list (used by default by
// Hatch and PDM, and supported by Poetry too). R-DET-14 targets pyproject.toml
// at the ecosystem level, so both dialects must be recognized.
var pep621DepsArrayRe = regexp.MustCompile(`(?s)dependencies\s*=\s*\[(.*?)\]`)

// pep621ItemRe finds quoted requirement strings inside a dependencies array.
var pep621ItemRe = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)

// pep621NameRe extracts the bare package name from a PEP 508 requirement
// string (e.g. "psycopg2>=2.9.9" or "boto3[s3]==1.34" -> "psycopg2"/"boto3").
var pep621NameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+`)

// gemRe finds a Gemfile's `gem "name"` declarations. The leading `^\s*gem`
// anchor is what keeps a commented-out `# gem "mysql2"` from being read as
// a declaration — inventing a dependency the repo deliberately dropped.
var gemRe = regexp.MustCompile(`(?m)^\s*gem\s+["']([A-Za-z0-9_.-]+)["']`)

// gemfileLockDepsRe captures the body of a Gemfile.lock's DEPENDENCIES
// section — the file's record of the *direct* dependencies, i.e. what the
// Gemfile itself declared. The `GEM ... specs:` block above it is the
// resolved transitive closure and is deliberately not read (R-DET-14).
var gemfileLockDepsRe = regexp.MustCompile(`(?s)\nDEPENDENCIES\n(.*?)\n\n`)

// gemfileLockDepRe extracts a gem name from one DEPENDENCIES line, e.g.
// "  pg (~> 1.5)" or "  rails!".
var gemfileLockDepRe = regexp.MustCompile(`(?m)^\s+([A-Za-z0-9_.-]+)`)

// mavenPOM is the part of a pom.xml R-DET-1 cares about. The
// `dependencies>dependency` path is a direct child path, so a
// <dependencyManagement> block — which pins versions for modules that may
// never depend on the library — is excluded by construction, and <modules>
// is captured so an aggregator pom can report what it did not read.
type mavenPOM struct {
	XMLName      xml.Name `xml:"project"`
	Modules      []string `xml:"modules>module"`
	Dependencies []struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
	} `xml:"dependencies>dependency"`
}

// gemNames extracts the direct gem names declared by a Ruby project in dir:
// the Gemfile's `gem` lines, or — when only the lockfile is present — the
// lockfile's DEPENDENCIES section, which lists the same direct set.
func gemNames(dir string) ([]string, error) {
	if fileExists(dir, "Gemfile") {
		raw, err := os.ReadFile(filepath.Join(dir, "Gemfile"))
		if err != nil {
			return nil, err
		}
		var names []string
		for _, m := range gemRe.FindAllStringSubmatch(string(raw), -1) {
			names = append(names, m[1])
		}
		return names, nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, "Gemfile.lock"))
	if err != nil {
		return nil, err
	}
	section := gemfileLockDepsRe.FindStringSubmatch("\n" + string(raw) + "\n\n")
	if section == nil {
		return nil, nil
	}
	var names []string
	for _, m := range gemfileLockDepRe.FindAllStringSubmatch(section[1], -1) {
		names = append(names, m[1])
	}
	return names, nil
}

// pep621PackageNames extracts every package name from a "dependencies = [...]"
// array in raw pyproject.toml content.
func pep621PackageNames(raw string) []string {
	var names []string
	for _, arr := range pep621DepsArrayRe.FindAllStringSubmatch(raw, -1) {
		for _, item := range pep621ItemRe.FindAllStringSubmatch(arr[1], -1) {
			req := item[1]
			if req == "" {
				req = item[2]
			}
			if name := pep621NameRe.FindString(req); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

// manifestInfo is one directory's manifest as detection read it.
type manifestInfo struct {
	// lang is the ecosystem ("go", "node", "python", "ruby", "java"), or
	// "" when the directory holds no manifest at all. The caller decides
	// what "" means — root and a service's build context read it
	// differently (see detectLockfiles and detectServiceManifests).
	lang string
	// file is the manifest's filename, for gap messages.
	file string
	// imports are the dependency names/import paths it declares.
	imports []string
	// clientTable is the client table to match imports against.
	clientTable []clientPattern
	// unread names dependency sources this manifest points at but that
	// R-DET-1 does not let us read — a Maven aggregator's modules, whose
	// own pom.xml files sit outside every compose-declared directory.
	// Non-empty means the facts this manifest would have decided are
	// undetermined, not false (R-COV-6), and it MUST become a gap.
	unread []string
}

// readManifest reads whichever R-DET-14 manifest (go.mod, package.json,
// pyproject.toml, Gemfile/Gemfile.lock, pom.xml) is present in dir. The
// order is only a tie-break for polyglot directories; each case is
// independent.
func readManifest(dir string) (manifestInfo, error) {
	var info manifestInfo
	switch {
	case fileExists(dir, "go.mod"):
		raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			return manifestInfo{}, err
		}
		info.lang, info.file, info.clientTable = "go", "go.mod", goClientPatterns
		for _, m := range goModRequireRe.FindAllStringSubmatch(string(raw), -1) {
			info.imports = append(info.imports, m[1])
		}

	case fileExists(dir, "package.json"):
		raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			return manifestInfo{}, err
		}
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal(raw, &pkg); err != nil {
			return manifestInfo{}, err
		}
		info.lang, info.file, info.clientTable = "node", "package.json", nodeClientPatterns
		for name := range pkg.Dependencies {
			info.imports = append(info.imports, name)
		}
		for name := range pkg.DevDependencies {
			info.imports = append(info.imports, name)
		}

	case fileExists(dir, "pyproject.toml"):
		raw, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
		if err != nil {
			return manifestInfo{}, err
		}
		info.lang, info.file, info.clientTable = "python", "pyproject.toml", pyClientPatterns
		for _, m := range pyprojectKeyRe.FindAllStringSubmatch(string(raw), -1) {
			info.imports = append(info.imports, m[1])
		}
		info.imports = append(info.imports, pep621PackageNames(string(raw))...)

	case fileExists(dir, "Gemfile") || fileExists(dir, "Gemfile.lock"):
		names, err := gemNames(dir)
		if err != nil {
			return manifestInfo{}, err
		}
		info.lang, info.file, info.clientTable = "ruby", "Gemfile", rubyClientPatterns
		if !fileExists(dir, "Gemfile") {
			info.file = "Gemfile.lock"
		}
		info.imports = names

	case fileExists(dir, "pom.xml"):
		raw, err := os.ReadFile(filepath.Join(dir, "pom.xml"))
		if err != nil {
			return manifestInfo{}, err
		}
		var pom mavenPOM
		if err := xml.Unmarshal(raw, &pom); err != nil {
			return manifestInfo{}, fmt.Errorf("parse %s: %w", filepath.Join(dir, "pom.xml"), err)
		}
		info.lang, info.file, info.clientTable = "java", "pom.xml", javaClientPatterns
		for _, d := range pom.Dependencies {
			info.imports = append(info.imports, d.GroupID+":"+d.ArtifactID)
		}
		info.unread = pom.Modules
	}
	return info, nil
}

// detectLockfiles reads the compose-project root's language manifest
// (R-DET-1, R-DET-14), sets sys.Lang and the manifest-dependent Coverage
// facts, and attaches client libraries to dependencies (R-DET-5). service
// is the compose service whose build context is this same root directory,
// if any (from serviceForDir) — a client found here is attributed to it,
// e.g. a single-service repo's api built with `build: .` right where its
// own go.mod lives.
func detectLockfiles(dir string, sys *System, service string) error {
	info, err := readManifest(dir)
	if err != nil {
		return err
	}
	sys.Lang = info.lang

	// A manifest that was read decides platform:aws/azure and the
	// otel-client half of lacks:otel; absent one, there is nothing that
	// could declare a dependency, so absence is verified, not guessed.
	sys.Coverage.AWS = FactFalse
	sys.Coverage.Azure = FactFalse
	for _, imp := range info.imports {
		matchClient(sys, info.clientTable, imp, service)
		if hasImportPrefix(imp, awsSDKImports[sys.Lang]) {
			sys.Coverage.AWS = FactTrue
		}
		if hasImportPrefix(imp, azureSDKImports[sys.Lang]) {
			sys.Coverage.Azure = FactTrue
		}
		if hasImportPrefix(imp, otelClientImports[sys.Lang]) {
			sys.otelClientSeen = true
		}
	}

	if len(info.unread) > 0 {
		// R-COV-6: the manifest points at dependency sources R-DET-1 does
		// not let us read, and they may well declare an AWS/Azure SDK or an
		// OTel client. That is "undetermined", not "verified absent":
		// defaulting to false here would, for lacks:otel, actively
		// recommend OTel setup to a team that already has it.
		if sys.Coverage.AWS == FactFalse {
			sys.Coverage.AWS = FactUnknown
		}
		if sys.Coverage.Azure == FactFalse {
			sys.Coverage.Azure = FactUnknown
		}
		if !sys.otelClientSeen {
			sys.otelClientUnknown = true
		}
		sys.Gaps = append(sys.Gaps, unreadGap(info, "", dir))
	}

	return nil
}

// unreadGap phrases the R-DET-7 gap for a manifest whose declared
// dependency sources lie outside what R-DET-1 permits reading. service is
// "" for the compose-project root.
func unreadGap(info manifestInfo, service, dir string) string {
	where := ""
	if service != "" {
		where = fmt.Sprintf(" in %s's build context (%s)", service, dir)
	}
	return fmt.Sprintf(
		"%s%s declares modules whose own manifests were not read (%s) — client libraries not detected (R-DET-1 reads only compose-declared directories)",
		info.file, where, strings.Join(info.unread, ", "))
}

// serviceForDir reports the compose service whose build context resolves
// to dir, or "" if none does (e.g. dir is the project root and every
// service builds from a subdirectory, or every service pulls an image).
func serviceForDir(services map[string]types.ServiceConfig, dir string) string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	target := filepath.Clean(dir)
	for _, name := range names {
		svc := services[name]
		if svc.Build != nil && svc.Build.Context != "" && filepath.Clean(svc.Build.Context) == target {
			return name
		}
	}
	return ""
}

// detectServiceManifests reads each compose service's own build-context
// manifest — a bounded, compose-declared location, never a general tree
// walk (R-DET-1) — and attributes any client library found there to that
// service (R-DET-5). This is the fix for the common monorepo/multi-service
// layout (docker-compose.yml at the root, each service's own go.mod under
// its build context) that the project-root-only scan in detectLockfiles
// cannot see. rootDir is skipped here since detectLockfiles already
// covers it, keyed by whichever service (if any) claims it as its own
// build context.
func detectServiceManifests(services map[string]types.ServiceConfig, rootDir string, sys *System) error {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)

	sutLang := ""
	var otherLangs []string

	root := filepath.Clean(rootDir)
	for _, name := range names {
		svc := services[name]
		if svc.Build == nil || svc.Build.Context == "" {
			continue
		}
		ctxDir := filepath.Clean(svc.Build.Context)
		if ctxDir == root {
			continue // already covered by detectLockfiles
		}

		info, err := readManifest(ctxDir)
		if err != nil {
			return err
		}
		if info.lang == "" {
			// R-DET-7: a service's build context having no manifest is not
			// itself a gap — a service may legitimately have none (e.g. a
			// prebuilt static binary, or an ecosystem outside R-DET-1's
			// list).
			continue
		}
		if name == sys.SUT {
			sutLang = info.lang
		} else if !slices.Contains(otherLangs, info.lang) {
			otherLangs = append(otherLangs, info.lang)
		}
		for _, imp := range info.imports {
			matchClient(sys, info.clientTable, imp, name)
		}
		if len(info.unread) > 0 {
			// A manifest we could only partly follow MUST still surface
			// rather than vanish — the same rule R-DET-14 applies at the
			// compose-project root.
			sys.Gaps = append(sys.Gaps, unreadGap(info, name, ctxDir))
		}
	}

	resolveLang(sys, sutLang, otherLangs)
	return nil
}

// resolveLang settles System.Lang from the manifests read above, per
// R-DET-17's order: the compose-project root first (already set by
// detectLockfiles, and never overridden), then the SUT's own build context,
// then the other services if they agree, then nothing plus a gap.
//
// The last step is a refusal rather than a fallback: Lang decides which
// knobs a verdict candidate names (internal/run), whether `emit fixtures`
// and `emit testcontainers` will generate at all, and which manifest
// doctor attributes a knob to. A wrong language there is wrong knobs and an
// emitted fixture that does not compile, so a polyglot stack whose SUT has
// no manifest gets both languages named and neither chosen.
func resolveLang(sys *System, sutLang string, otherLangs []string) {
	if sys.Lang != "" || sutLang == "" && len(otherLangs) == 0 {
		return
	}
	if sutLang != "" {
		// The SUT is the thing under test, so its own manifest decides even
		// when other services disagree — that disagreement is not a
		// question about the SUT.
		sys.Lang = sutLang
		return
	}
	if len(otherLangs) == 1 {
		sys.Lang = otherLangs[0]
		return
	}
	sorted := slices.Clone(otherLangs)
	slices.Sort(sorted)
	sys.Gaps = append(sys.Gaps, fmt.Sprintf(
		"language not determined: the SUT (%s) has no manifest R-DET-1 can read and the other services disagree (%s) — knobs, `emit fixtures` and `emit testcontainers` need one, so name it rather than let a wrong guess emit code that does not compile",
		sutName(sys), strings.Join(sorted, ", ")))
}

// sutName renders the SUT for a message, being explicit when there is none
// rather than leaving an empty pair of brackets.
func sutName(sys *System) string {
	if sys.SUT == "" {
		return "none detected"
	}
	return sys.SUT
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// matchClient attaches imp to a dependency if it matches any pattern in
// table, per the client column of SPEC.md §3.1. service is the compose
// service whose manifest imp was read from ("" for the project root when
// no service claims it as a build context).
func matchClient(sys *System, table []clientPattern, imp, service string) {
	for _, cp := range table {
		if hasImportPrefix(imp, cp.imports) {
			attachClient(sys, cp.typ, imp, service)
		}
	}
}

func hasImportPrefix(imp string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(imp, p) {
			return true
		}
	}
	return false
}

// attachClient records imp as a client of the dependency of type typ,
// attributed to service (R-DET-5, via ClientRefs), creating a
// lockfile-only dependency per R-DET-13 if none exists yet. R-DET-10: a
// type not already present via compose is only fabricated here when its
// only source is lockfile.
func attachClient(sys *System, typ, imp, service string) {
	ref := ClientRef{Import: imp, Service: service}
	for i := range sys.Deps {
		if sys.Deps[i].Type == typ {
			sys.Deps[i].Clients = append(sys.Deps[i].Clients, imp)
			sys.Deps[i].ClientRefs = append(sys.Deps[i].ClientRefs, ref)
			return
		}
	}
	if !lockfileOnly[typ] {
		return
	}
	sys.Deps = append(sys.Deps, Dep{Name: typ, Type: typ, Clients: []string{imp}, ClientRefs: []ClientRef{ref}})
}
