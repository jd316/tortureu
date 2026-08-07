package detect

import (
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

// goModRequireRe finds "<module path> vX.Y.Z" pairs, matching both the
// single-line and block forms of a require directive.
var goModRequireRe = regexp.MustCompile(`(\S+)\s+v\d+\.\d+\.\d+\S*`)

// detectLockfiles reads language manifests (R-DET-1) in dir, sets sys.Lang,
// and attaches client libraries to dependencies (R-DET-5).
func detectLockfiles(dir string, sys *System) error {
	goModPath := filepath.Join(dir, "go.mod")
	raw, err := os.ReadFile(goModPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sys.Lang = "go"

	imports := goModRequireRe.FindAllStringSubmatch(string(raw), -1)
	for _, m := range imports {
		imp := m[1]
		for _, cp := range goClientPatterns {
			if !hasImportPrefix(imp, cp.imports) {
				continue
			}
			attachClient(sys, cp.typ, imp)
		}
	}
	return nil
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
