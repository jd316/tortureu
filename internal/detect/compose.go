// Package detect turns a repo into a System description.
//
// D-3 caps detection at docker-compose + lockfile. Anything it cannot classify
// is reported as a gap, never guessed.
package detect

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
)

// depPattern is one recognizer for the R-DET-9 vocabulary: an image whose
// (tag-stripped) reference matches pattern is classified as typ.
type depPattern struct {
	typ     string
	pattern string
}

// imagePatterns implements the "Recognized by" image column of SPEC.md §3.1.
// Order matters only in that the first match wins; the table has no
// overlapping patterns so this is not currently load-bearing.
var imagePatterns = []depPattern{
	{"postgresql", "postgres*"},
	{"mysql", "mysql*"},
	{"mysql", "mariadb*"},
	{"redis", "redis*"},
	{"redis", "valkey*"},
	{"mongodb", "mongo*"},
	{"kafka", "*kafka*"},
	{"kafka", "redpanda*"},
	{"rabbitmq", "rabbitmq*"},
	{"nats", "nats*"},
	{"elasticsearch", "elasticsearch*"},
	{"elasticsearch", "opensearch*"},
	{"cassandra", "cassandra*"},
	{"cassandra", "scylla*"},
	{"cockroach", "cockroachdb*"},
	{"etcd", "*etcd*"},
	{"consul", "consul*"},
	{"zookeeper", "zookeeper*"},
	{"oracle", "*oracle*"},
	{"oracle", "*oracledb*"},
	{"minio", "minio*"},
	{"mqtt", "mosquitto*"},
	{"mqtt", "emqx*"},
	{"aws", "localstack*"},
	{"smtp", "mailhog*"},
	{"smtp", "mailpit*"},
}

// obsPattern is one recognizer for R-DET-12 observability infrastructure.
type obsPattern struct {
	pattern string
	traces  bool
	metrics bool
	logs    bool
}

var obsPatterns = []obsPattern{
	{"jaeger*", true, false, false},
	{"tempo*", true, false, false},
	{"zipkin*", true, false, false},
	{"prom/prometheus*", false, true, false},
	{"victoriametrics*", false, true, false},
	{"grafana/loki*", false, false, true},
	{otelCollectorPattern, true, true, false},
}

// defaultPorts gives the well-known port for a dependency type, used to
// derive an address (R-DET-4) for a compose service that does not publish
// one explicitly.
var defaultPorts = map[string]string{
	"postgresql":    "5432",
	"mysql":         "3306",
	"redis":         "6379",
	"mongodb":       "27017",
	"kafka":         "9092",
	"rabbitmq":      "5672",
	"nats":          "4222",
	"elasticsearch": "9200",
	"cassandra":     "9042",
	"cockroach":     "26257",
	"etcd":          "2379",
	"consul":        "8500",
	"zookeeper":     "2181",
	"oracle":        "1521",
	"minio":         "9000",
	"mqtt":          "1883",
	"smtp":          "1025",
}

// Detect reads a compose file (via compose-go, R-DET-11) and language
// manifests (R-DET-1) to describe the system.
func Detect(composePath string) (*System, error) {
	absPath, err := filepath.Abs(composePath)
	if err != nil {
		return nil, err
	}
	workingDir := filepath.Dir(absPath)

	ctx := context.Background()
	configDetails, err := loader.LoadConfigFiles(ctx, []string{absPath}, workingDir)
	if err != nil {
		return nil, err
	}
	project, err := loader.LoadWithContext(ctx, *configDetails, func(o *loader.Options) {
		o.SkipValidation = true
		o.SkipConsistencyCheck = true
	})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	sys := &System{}
	for _, name := range names {
		svc := project.Services[name]

		// R-DET-8: build+image is the SUT, never a dependency, even though
		// it also has an image tag (the build's output, not a pull ref).
		if svc.Build != nil {
			sys.SUT = name
			continue
		}
		if svc.Image == "" {
			continue
		}

		if ot, ok := matchObs(svc.Image); ok {
			sys.Obs.Traces = sys.Obs.Traces || ot.traces
			sys.Obs.Metrics = sys.Obs.Metrics || ot.metrics
			sys.Obs.Logs = sys.Obs.Logs || ot.logs
			if isOtelCollector(svc.Image) {
				sys.otelCollectorSeen = true
			}
			continue
		}

		t := depType(svc.Image)
		if t == "" {
			sys.Gaps = append(sys.Gaps, fmt.Sprintf(
				"unrecognized image %s (service %s) — classify it in torture.yaml", svc.Image, name))
			continue
		}

		dep := Dep{Name: name, Type: t}
		// R-DET-4: prefer the compose-declared container port over the
		// well-known-port table — a service running its dependency on a
		// non-default port would otherwise get a wrong address.
		if len(svc.Ports) > 0 && svc.Ports[0].Target != 0 {
			dep.Address = fmt.Sprintf("%s:%d", name, svc.Ports[0].Target)
		} else if port, ok := defaultPorts[t]; ok {
			dep.Address = name + ":" + port
		}
		if dep.Address != "" {
			sys.Egress = append(sys.Egress, dep.Address)
			if sys.EgressClass == nil {
				sys.EgressClass = map[string]string{}
			}
			sys.EgressClass[dep.Address] = "internal"
		}
		sys.Deps = append(sys.Deps, dep)
	}

	if sys.Obs.Traces {
		sys.Obs.MaxConfidence = "caused"
	} else if sys.Obs.Metrics || sys.Obs.Logs {
		sys.Obs.MaxConfidence = "correlated"
	}

	if err := detectLockfiles(workingDir, sys); err != nil {
		return nil, err
	}

	if err := detectFileCoverage(workingDir, sys); err != nil {
		return nil, err
	}

	// R-COV-5 lacks:otel, tri-state (R-COV-6): a verified collector or
	// client wins outright — an OTel collector in compose proves the system
	// has OTel regardless of whether its manifest could be parsed. Only
	// once neither is confirmed does an unparsed manifest make the fact
	// unknown rather than true.
	switch {
	case sys.otelClientSeen || sys.otelCollectorSeen:
		sys.Coverage.LacksOtel = FactFalse
	case sys.otelClientUnknown:
		sys.Coverage.LacksOtel = FactUnknown
	default:
		sys.Coverage.LacksOtel = FactTrue
	}

	return sys, nil
}

// depType normalizes an image reference to an R-DET-9 dependency type, or
// "" if it does not match the closed vocabulary.
func depType(image string) string {
	ref := stripTag(image)
	for _, p := range imagePatterns {
		if matchPattern(ref, p.pattern) {
			return p.typ
		}
	}
	return ""
}

// matchObs reports whether image is recognized observability infrastructure
// per R-DET-12, and if so what coverage it provides.
func matchObs(image string) (obsPattern, bool) {
	ref := stripTag(image)
	for _, p := range obsPatterns {
		if matchPattern(ref, p.pattern) {
			return p, true
		}
	}
	return obsPattern{}, false
}

// otelCollectorPattern is the R-DET-12 image pattern for the OTel collector,
// reused by R-COV-5's lacks:otel fact to distinguish "collector present"
// from the other observability backends (jaeger, prometheus, loki, ...).
const otelCollectorPattern = "otel/opentelemetry-collector*"

// isOtelCollector reports whether image is specifically the OTel collector.
func isOtelCollector(image string) bool {
	return matchPattern(stripTag(image), otelCollectorPattern)
}

// stripTag removes a trailing ":tag" from an image reference, being careful
// not to mistake a "registry:port/name" host port for a tag.
func stripTag(image string) string {
	i := strings.LastIndex(image, ":")
	if i < 0 {
		return image
	}
	if strings.Contains(image[i:], "/") {
		return image
	}
	return image[:i]
}

// matchPattern matches s against pattern, where pattern may carry a leading
// and/or trailing '*' wildcard (SPEC.md §3.1's "foo*", "*foo*" notation).
func matchPattern(s, pattern string) bool {
	prefixWild := strings.HasPrefix(pattern, "*")
	suffixWild := strings.HasSuffix(pattern, "*")
	core := strings.TrimSuffix(strings.TrimPrefix(pattern, "*"), "*")
	switch {
	case prefixWild && suffixWild:
		return strings.Contains(s, core)
	case suffixWild:
		return strings.HasPrefix(s, core)
	case prefixWild:
		return strings.HasSuffix(s, core)
	default:
		return s == core
	}
}
