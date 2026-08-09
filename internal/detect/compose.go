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
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
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
// manifests (R-DET-1) to describe the system, letting R-DET-19 decide the
// SUT where more than one service declares build:.
func Detect(composePath string) (*System, error) {
	return DetectWithSUT(composePath, "")
}

// DetectWithSUT is Detect with the system under test named explicitly,
// which settles R-DET-19's ambiguity outright: a caller who says which
// service is under test is never overruled by a derived pick. A sutService
// that is not in the compose file is an error naming the services that are
// — falling back would run everything against something the caller did not
// ask for, having said nothing about it.
func DetectWithSUT(composePath, sutService string) (*System, error) {
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

	// R-DET-19: decide the SUT before classifying anything, so that the
	// ports recorded for base_url (R-DET-16) belong to the service actually
	// chosen rather than to whichever build: service the scan saw last.
	if err := chooseSUT(sys, project.Services, names, sutService); err != nil {
		return nil, err
	}

	for _, name := range names {
		svc := project.Services[name]

		if name == sys.SUT {
			sys.SUTPorts = listenPorts(svc.Ports, svc.Expose)
			continue
		}
		// R-DET-8: a build: service is the SUT and never a dependency, even
		// though it may also carry an image tag (the build's output, not a
		// pull ref) — and that holds for the build: services R-DET-19 did
		// not choose too: they are neither dependency nor gap.
		if svc.Build != nil {
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

	// R-DET-6 / TBD-6: the floor is "correlated", never "" and never a
	// distinct "none". TortureU schedules the faults and k6 measures the
	// breach, so D-4's single-fault time-window attribution holds with no
	// cooperation from the target; traces are what raise the ceiling to
	// "caused". An empty value would JSON-omit itself (max_confidence is
	// omitempty in both internal/mcp and internal/verdict) and render as a
	// blank field, telling the repos that most need to hear about their
	// confidence ceiling nothing at all.
	sys.Obs.MaxConfidence = "correlated"
	if sys.Obs.Traces {
		sys.Obs.MaxConfidence = "caused"
	}

	// R-DET-4: a host named in a service's environment: or env_file that
	// isn't itself an in-compose service is an external host — the
	// realistic way an app is told which partner API to call.
	internalNames := make(map[string]bool, len(names))
	for _, name := range names {
		internalNames[name] = true
	}
	detectExternalHosts(project.Services, workingDir, internalNames, sys)

	// R-DET-1: manifests are read from the compose project root and, per
	// the extension below, each service's own declared build context —
	// both are bounded, compose-declared locations, never a tree walk.
	rootService := serviceForDir(project.Services, workingDir)
	if err := detectLockfiles(workingDir, sys, rootService); err != nil {
		return nil, err
	}
	if err := detectServiceManifests(project.Services, workingDir, sys); err != nil {
		return nil, err
	}

	if err := detectFileCoverage(workingDir, sys); err != nil {
		return nil, err
	}

	// R-DET-18: every manifest R-DET-1 permits has now been read — the
	// project root's and each service's build context — so platform:aws and
	// platform:azure can be settled. Not before: one manifest's "not here"
	// cannot tell absence from not-looked-there (R-COV-6).
	finalizeManifestFacts(sys)

	// R-COV-5 lacks:otel, tri-state (R-COV-6): a verified collector or
	// client wins outright — an OTel collector in compose proves the system
	// has OTel regardless of whether its manifest could be parsed. Only
	// once neither is confirmed does an unread manifest make the fact
	// unknown rather than true.
	switch {
	case sys.otelClientSeen || sys.otelCollectorSeen:
		sys.Coverage.LacksOtel = FactFalse
	case sys.manifestUnread:
		sys.Coverage.LacksOtel = FactUnknown
	default:
		sys.Coverage.LacksOtel = FactTrue
	}

	return sys, nil
}

// chooseSUT settles which compose service is the system under test and
// records how, per R-DET-19. The choice is evidence-based and visible,
// because everything downstream hangs off it: the fault targets, the verdict,
// and target.base_url.
//
// requested wins outright. Otherwise a single build: service is the SUT with
// nothing reported (R-DET-8's plain case, the overwhelming one). Where
// several build:, the candidates are those no other service depends_on — an
// application sits above its dependencies, so load enters the graph at the
// top — and if that is exactly one it is chosen and marked derived. If it is
// not, no SUT is chosen and every candidate is reported: `run` refuses a
// config with no target.service in one line, whereas a wrong one silently
// tortures the wrong container and produces a verdict about it.
func chooseSUT(sys *System, services types.Services, names []string, requested string) error {
	if requested != "" {
		if _, ok := services[requested]; !ok {
			return fmt.Errorf("service %q is not in the compose file; it declares %s",
				requested, strings.Join(names, ", "))
		}
		sys.SUT, sys.SUTChoice = requested, SUTChoiceRequested
		return nil
	}

	var builds []string
	for _, name := range names {
		if services[name].Build != nil {
			builds = append(builds, name)
		}
	}

	switch len(builds) {
	case 0:
		// R-CLI-4/R-DET-8: init already reports "no service declares build:"
		// for this; there is nothing to choose.
		sys.SUTChoice = SUTChoiceNone
		return nil
	case 1:
		sys.SUT, sys.SUTChoice = builds[0], SUTChoiceOnly
		return nil
	}

	dependedOn := map[string]bool{}
	for _, name := range names {
		for dep := range services[name].DependsOn {
			dependedOn[dep] = true
		}
	}
	var roots []string
	for _, name := range builds {
		if !dependedOn[name] {
			roots = append(roots, name)
		}
	}

	sys.SUTCandidates = builds
	if len(roots) == 1 {
		sys.SUT, sys.SUTChoice = roots[0], SUTChoiceDerived
		return nil
	}

	sys.SUTChoice = SUTChoiceUndecided
	sys.Gaps = append(sys.Gaps, fmt.Sprintf(
		"system under test not decided: %d compose services declare build: (%s) and the depends_on graph does not single one out — name it with `-service <name>`, because a wrong guess tortures the wrong container and reports a verdict about it",
		len(builds), strings.Join(builds, ", ")))
	return nil
}

// listenPorts returns the ports a service itself listens on, per R-DET-16:
// the container side (Target) of each ports: entry, plus every expose:
// entry, deduplicated and in declaration order.
//
// The container side is what target.base_url needs, even though a base URL
// looks host-shaped: internal/run dials it from inside the SUT container's
// own network namespace (k6's `--network container:<id>`, R-DC2-3's fix, and
// --fuzz's identical attachment), where the published host port is not bound
// at all. Dep.Address reads the same field for a different reason — a
// dependency is dialled from inside the compose network (R-DET-4).
//
// Dropped, because none of them names one reachable TCP port: a zero
// target, a non-TCP port, and a port range (`8000-8010`, which compose-go
// keeps unexpanded) — which member of a range serves the API is exactly
// what compose does not say, and R-CLI-19 refuses to guess.
func listenPorts(ports []types.ServicePortConfig, expose []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(port string) {
		if port == "" || port == "0" || strings.Contains(port, "-") || seen[port] {
			return
		}
		seen[port] = true
		out = append(out, port)
	}

	for _, p := range ports {
		if p.Protocol != "" && p.Protocol != "tcp" {
			continue
		}
		if p.Target == 0 {
			continue
		}
		add(strconv.FormatUint(uint64(p.Target), 10))
	}
	for _, e := range expose {
		// expose: entries keep their raw compose spelling, which may carry
		// a protocol suffix ("3000/udp").
		port, proto, hasProto := strings.Cut(e, "/")
		if hasProto && proto != "tcp" {
			continue
		}
		add(port)
	}
	return out
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
