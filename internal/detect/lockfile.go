package detect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// clientPattern recognizes a client library import as belonging to a
// dependency type, per the client column of SPEC.md §3.1. Only Go imports
// are recognized: R-DET-1 caps manifest reading, and go.mod is the only
// manifest exercised so far.
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

// unsupportedManifests are R-DET-1 manifests deferred past v0 (TBD-7). A
// present-but-unsupported manifest MUST be reported as a gap (R-DET-14),
// never silently ignored.
var unsupportedManifests = []string{"Gemfile", "pom.xml"}

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

// detectLockfiles reads language manifests (R-DET-1, capped to the R-DET-14
// v0 set: go.mod, package.json, pyproject.toml), sets sys.Lang, and attaches
// client libraries to dependencies (R-DET-5).
func detectLockfiles(dir string, sys *System) error {
	switch {
	case fileExists(dir, "go.mod"):
		raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			return err
		}
		sys.Lang = "go"
		for _, m := range goModRequireRe.FindAllStringSubmatch(string(raw), -1) {
			matchClient(sys, goClientPatterns, m[1])
		}

	case fileExists(dir, "package.json"):
		raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			return err
		}
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if err := json.Unmarshal(raw, &pkg); err != nil {
			return err
		}
		sys.Lang = "node"
		for name := range pkg.Dependencies {
			matchClient(sys, nodeClientPatterns, name)
		}
		for name := range pkg.DevDependencies {
			matchClient(sys, nodeClientPatterns, name)
		}

	case fileExists(dir, "pyproject.toml"):
		raw, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
		if err != nil {
			return err
		}
		sys.Lang = "python"
		for _, m := range pyprojectKeyRe.FindAllStringSubmatch(string(raw), -1) {
			matchClient(sys, pyClientPatterns, m[1])
		}
		for _, name := range pep621PackageNames(string(raw)) {
			matchClient(sys, pyClientPatterns, name)
		}
	}

	for _, name := range unsupportedManifests {
		if fileExists(dir, name) {
			sys.Gaps = append(sys.Gaps, fmt.Sprintf(
				"unsupported manifest %s present — client libraries not detected (R-DET-14)", name))
		}
	}

	return nil
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// matchClient attaches imp to a dependency if it matches any pattern in
// table, per the client column of SPEC.md §3.1.
func matchClient(sys *System, table []clientPattern, imp string) {
	for _, cp := range table {
		if hasImportPrefix(imp, cp.imports) {
			attachClient(sys, cp.typ, imp)
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
// creating a lockfile-only dependency per R-DET-13 if none exists yet.
// R-DET-10: a type not already present via compose is only fabricated here
// when its only source is lockfile.
func attachClient(sys *System, typ, imp string) {
	for i := range sys.Deps {
		if sys.Deps[i].Type == typ {
			sys.Deps[i].Clients = append(sys.Deps[i].Clients, imp)
			return
		}
	}
	if !lockfileOnly[typ] {
		return
	}
	sys.Deps = append(sys.Deps, Dep{Name: typ, Type: typ, Clients: []string{imp}})
}
