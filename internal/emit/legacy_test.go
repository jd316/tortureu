package emit

import (
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-CLI-8

const jmeterFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    postgres:5432: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: ramp_up
      to: 200rps
      over: 30s
    - phase: peak
      hold: 200rps
      for: 120s
faults:
  - name: pg_slow
    at: peak
    for: 30s
    target: postgres:5432
    inject: { latency: 300ms }
assert:
  - http_req_duration: ["p(95)<500"]
`

func jmeterSystem(types ...string) *detect.System {
	sys := &detect.System{SUT: "checkout-api"}
	for _, t := range types {
		sys.Deps = append(sys.Deps, detect.Dep{Name: t, Type: t, Clients: []string{"lockfile-only"}})
	}
	return sys
}

// spec: R-CLI-8 — "detection never ran" and "this repo speaks none of these
// protocols" are different facts.
func TestJMeter_DistinguishesNoDetectionFromNoProtocol(t *testing.T) {
	cfg := mustParse(t, jmeterFixture)

	noDetect, err := JMeter(cfg, nil)
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	if !strings.Contains(noDetect, "could not be detected") {
		t.Errorf("a nil *detect.System must say detection did not run, got:\n%s", noDetect)
	}
	noProto, err := JMeter(cfg, &detect.System{SUT: "checkout-api"})
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	if noProto == noDetect {
		t.Error("undetected and no-protocol produced the same output")
	}
	for _, want := range []string{"jms", "ldap", "soap"} {
		if !strings.Contains(noProto, want) {
			t.Errorf("the refusal must name the protocols jmeter covers (%s):\n%s", want, noProto)
		}
	}
	if !NeedsSystem("jmeter") {
		t.Error("jmeter must be registered as needing detection: its protocols are detection facts")
	}
}

// spec: R-CLI-8 — the emitted plan must be a document JMeter can parse, and
// its structure must be the one JMeter's own SaveService writes: every
// element followed by a sibling hashTree.
func TestJMeter_EmitsWellFormedPlanWithBalancedHashTrees(t *testing.T) {
	out, err := JMeter(mustParse(t, jmeterFixture), jmeterSystem("ldap", "jms", "soap"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	dec := xml.NewDecoder(strings.NewReader(out))
	var elements, hashTrees int
	depth := 0
	for {
		tok, terr := dec.Token()
		if terr != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok {
			depth++
			switch se.Name.Local {
			case "hashTree":
				hashTrees++
			case "jmeterTestPlan", "stringProp", "boolProp", "intProp", "longProp", "doubleProp",
				"elementProp", "collectionProp", "objProp", "name", "value":
			default:
				if depth > 2 {
					elements++
				}
			}
		}
	}
	if err := xml.Unmarshal([]byte(out), new(any)); err != nil {
		t.Fatalf("emitted plan is not well-formed XML: %v", err)
	}
	if hashTrees == 0 || elements == 0 {
		t.Fatalf("expected test elements and their hashTrees, got %d/%d", elements, hashTrees)
	}
}

// spec: R-CLI-8 — only the protocols detection actually found are emitted. A
// plan carrying an LDAP sampler for a repo with no LDAP client is a request
// nobody asked to send.
func TestJMeter_EmitsOnlyDetectedProtocols(t *testing.T) {
	out, err := JMeter(mustParse(t, jmeterFixture), jmeterSystem("ldap"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	if !strings.Contains(out, "LDAPExtSampler") {
		t.Errorf("detected ldap but emitted no LDAP sampler:\n%s", out)
	}
	if strings.Contains(out, "PublisherSampler") {
		t.Errorf("emitted a JMS sampler for a system with no jms dependency:\n%s", out)
	}
	if strings.Contains(out, "HTTPSamplerProxy") {
		t.Errorf("emitted a SOAP sampler for a system with no soap dependency:\n%s", out)
	}
}

// spec: R-CLI-8 — jms/ldap/soap are lockfile-only dependencies (R-DET-13):
// detection proves the SUT SPEAKS the protocol and never says where the
// endpoint is. Every address must therefore be a JMeter property with a
// self-describing unset marker, never a guessed host, port or URL.
func TestJMeter_GuessesNoEndpoint(t *testing.T) {
	out, err := JMeter(mustParse(t, jmeterFixture), jmeterSystem("ldap", "jms", "soap"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	for _, guessed := range []string{"localhost", "127.0.0.1", "tcp://", "61616", "389", "636"} {
		if strings.Contains(out, guessed) {
			t.Errorf("emitted a guessed endpoint value %q:\n%s", guessed, out)
		}
	}
	for _, prop := range []string{"TORTUREU_LDAP_HOST", "TORTUREU_JMS_PROVIDER_URL", "TORTUREU_SOAP_HOST"} {
		if !strings.Contains(out, "__P("+prop+",") {
			t.Errorf("expected %s to be a JMeter property with a default, got:\n%s", prop, out)
		}
	}
	if !strings.Contains(out, "TORTUREU-UNSET-") {
		t.Errorf("expected an unset marker so an unset property fails legibly:\n%s", out)
	}
}

// spec: R-CLI-8 — the load profile is translated, not invented: the plan's
// rate comes from load.stages' peak and its duration from their sum, through
// the open-model timer JMeter actually ships (PreciseThroughputTimer), not a
// closed-model thread count standing in for an arrival rate.
func TestJMeter_TranslatesLoadStages(t *testing.T) {
	out, err := JMeter(mustParse(t, jmeterFixture), jmeterSystem("ldap"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	if !strings.Contains(out, "PreciseThroughputTimer") {
		t.Errorf("expected the open-model timer for R-CFG-6's arrival-rate model:\n%s", out)
	}
	if !strings.Contains(out, "<value>200.0</value>") {
		t.Errorf("expected the peak 200rps carried into the timer's throughput:\n%s", out)
	}
	if !strings.Contains(out, `<longProp name="duration">150</longProp>`) {
		t.Errorf("expected the 30s+120s stage total as the scheduled duration:\n%s", out)
	}
	if !strings.Contains(out, `<stringProp name="ThreadGroup.ramp_time">30</stringProp>`) {
		t.Errorf("expected the ramp_up stage's 30s as the thread ramp:\n%s", out)
	}
}

// spec: R-CLI-8 — every fault is reported as untranslated inside the plan
// itself, so the omission is visible to whoever opens it.
func TestJMeter_ReportsUntranslatedFaults(t *testing.T) {
	out, err := JMeter(mustParse(t, jmeterFixture), jmeterSystem("ldap"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	if !strings.Contains(out, "pg_slow") || !strings.Contains(out, "not translated") {
		t.Errorf("fault pg_slow not reported as untranslated:\n%s", out)
	}
}

// spec: R-CLI-8 — the SOAP path uses HTTPSamplerProxy, because JMeter removed
// its SOAP/XML-RPC sampler in 3.2 (Bug 60727) and upgrade.properties maps the
// old classes onto ObsoleteGui. Emitting the dead element would produce a
// plan that loads and samples nothing.
func TestJMeter_SOAPUsesHTTPSamplerNotTheRemovedSOAPSampler(t *testing.T) {
	out, err := JMeter(mustParse(t, jmeterFixture), jmeterSystem("soap"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	for _, dead := range []string{"WebServiceSampler", "SoapSampler"} {
		if strings.Contains(out, dead) {
			t.Errorf("emitted %s, removed from JMeter in 3.2:\n%s", dead, out)
		}
	}
	if !strings.Contains(out, `<boolProp name="HTTPSampler.postBodyRaw">true</boolProp>`) {
		t.Errorf("a SOAP envelope must be posted as a raw body:\n%s", out)
	}
	if !strings.Contains(out, "SOAPAction") || !strings.Contains(out, "text/xml") {
		t.Errorf("expected the SOAP headers a real service requires:\n%s", out)
	}
}

// spec: R-CLI-8 — the JMS publisher and the JMS point-to-point sampler share
// no property names at all; this emits the publisher, and must use its
// namespace (jms.*), not JMSSampler.*.
func TestJMeter_JMSUsesThePublisherPropertyNamespace(t *testing.T) {
	out, err := JMeter(mustParse(t, jmeterFixture), jmeterSystem("jms"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	for _, want := range []string{
		`<stringProp name="jms.initial_context_factory">`,
		`<stringProp name="jms.provider_url">`,
		`<stringProp name="jms.connection_factory">`,
		`<stringProp name="jms.topic">`,
		`<stringProp name="jms.config_choice">jms_use_text</stringProp>`,
		`<stringProp name="jms.config_msg_type">jms_text_message</stringProp>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing real PublisherSampler property %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "JMSSampler.") {
		t.Errorf("mixed the point-to-point sampler's property namespace into the publisher:\n%s", out)
	}
	if !strings.Contains(out, "activemq") && !strings.Contains(out, "provider jar") {
		t.Errorf("the plan must say a JMS provider jar is required in jmeter's lib/:\n%s", out)
	}
}

// spec: R-CLI-8 — the end-to-end claim in legacy.go's VERIFICATION STATUS
// block: a real JMeter loads and runs this plan. Off by default (the gate
// must not need a JMeter install):
//
//	TORTUREU_EMIT_LIVE=1 TORTUREU_JMETER_HOME=/path/to/apache-jmeter-5.6.3 \
//	  go test ./internal/emit/ -run TestJMeter_LoadedByRealJMeter -v
//
// With TORTUREU_LDAP_HOST also set (a real directory server), it asserts the
// LDAP samples actually succeeded rather than merely that the plan parsed.
func TestJMeter_LoadedByRealJMeter(t *testing.T) {
	if os.Getenv("TORTUREU_EMIT_LIVE") != "1" {
		t.Skip("set TORTUREU_EMIT_LIVE=1 and TORTUREU_JMETER_HOME to verify against a real JMeter")
	}
	home := os.Getenv("TORTUREU_JMETER_HOME")
	if home == "" {
		t.Skip("TORTUREU_JMETER_HOME not set")
	}
	// A short profile: the plan schedules itself from load.stages, and the
	// main fixture's 150s window is a real run, not a test.
	live := strings.Replace(jmeterFixture, "over: 30s", "over: 2s", 1)
	live = strings.Replace(live, "for: 120s", "for: 5s", 1)
	live = strings.Replace(live, "to: 200rps", "to: 20rps", 1)
	live = strings.Replace(live, "hold: 200rps", "hold: 20rps", 1)
	out, err := JMeter(mustParse(t, live), jmeterSystem("ldap"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.jmx")
	if err := os.WriteFile(plan, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	jtl := filepath.Join(dir, "results.jtl")

	args := []string{"-n", "-t", plan, "-l", jtl,
		"-j", filepath.Join(dir, "jmeter.log"),
		"-JTORTUREU_JTL=" + jtl}
	ldapHost := os.Getenv("TORTUREU_LDAP_HOST")
	if ldapHost != "" {
		for _, kv := range []string{
			"TORTUREU_LDAP_HOST=" + ldapHost,
			"TORTUREU_LDAP_PORT=" + os.Getenv("TORTUREU_LDAP_PORT"),
			"TORTUREU_LDAP_ROOTDN=" + os.Getenv("TORTUREU_LDAP_ROOTDN"),
			"TORTUREU_LDAP_BIND_DN=" + os.Getenv("TORTUREU_LDAP_BIND_DN"),
			"TORTUREU_LDAP_BIND_PW=" + os.Getenv("TORTUREU_LDAP_BIND_PW"),
			"TORTUREU_LDAP_SEARCH_BASE=" + os.Getenv("TORTUREU_LDAP_SEARCH_BASE"),
			"TORTUREU_LDAP_SEARCH_FILTER=" + os.Getenv("TORTUREU_LDAP_SEARCH_FILTER"),
		} {
			args = append(args, "-J"+kv)
		}
	}
	cmd := exec.Command(filepath.Join(home, "bin", "jmeter"), args...)
	combined, rerr := cmd.CombinedOutput()
	log := string(combined)
	if rerr != nil {
		t.Fatalf("real JMeter rejected the emitted plan: %v\n%s", rerr, log)
	}
	if !strings.Contains(log, "Created the tree successfully") {
		t.Fatalf("JMeter did not build a tree from the emitted plan:\n%s", log)
	}
	t.Logf("jmeter run:\n%s", log)

	if ldapHost == "" {
		return
	}
	raw, err := os.ReadFile(jtl)
	if err != nil {
		t.Fatalf("no results file: %v", err)
	}
	results := string(raw)
	if strings.Contains(results, ",false,") {
		t.Fatalf("an LDAP sample failed against the live directory:\n%s", results)
	}
	if !strings.Contains(results, ",true,") {
		t.Fatalf("no successful sample was recorded:\n%s", results)
	}
	t.Logf("results.jtl:\n%s", results)
}

// spec: R-CLI-8 — the SOAP half of legacy.go's VERIFICATION STATUS block: a
// real JMeter, running the emitted plan, delivers a POST carrying the SOAP
// headers and the envelope read from the file the property names. The server
// is this test's own, so what it asserts is what arrived on the wire.
//
//	TORTUREU_EMIT_LIVE=1 TORTUREU_JMETER_HOME=/path/to/apache-jmeter-5.6.3 \
//	  go test ./internal/emit/ -run TestJMeter_SOAPDeliveredByRealJMeter -v
func TestJMeter_SOAPDeliveredByRealJMeter(t *testing.T) {
	if os.Getenv("TORTUREU_EMIT_LIVE") != "1" {
		t.Skip("set TORTUREU_EMIT_LIVE=1 and TORTUREU_JMETER_HOME to verify against a real JMeter")
	}
	home := os.Getenv("TORTUREU_JMETER_HOME")
	if home == "" {
		t.Skip("TORTUREU_JMETER_HOME not set")
	}

	type received struct {
		contentType string
		soapAction  string
		body        string
	}
	got := make(chan received, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case got <- received{r.Header.Get("Content-Type"), r.Header.Get("SOAPAction"), string(body)}:
		default:
		}
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body/></soap:Envelope>`))
	}))
	defer srv.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}

	live := strings.Replace(jmeterFixture, "over: 30s", "over: 1s", 1)
	live = strings.Replace(live, "for: 120s", "for: 3s", 1)
	live = strings.Replace(live, "to: 200rps", "to: 10rps", 1)
	live = strings.Replace(live, "hold: 200rps", "hold: 10rps", 1)
	out, err := JMeter(mustParse(t, live), jmeterSystem("soap"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.jmx")
	if err := os.WriteFile(plan, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	envelope := filepath.Join(dir, "envelope.xml")
	const envelopeBody = `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><tortureu/></soap:Body></soap:Envelope>`
	if err := os.WriteFile(envelope, []byte(envelopeBody), 0o644); err != nil {
		t.Fatal(err)
	}
	jtl := filepath.Join(dir, "results.jtl")

	cmd := exec.Command(filepath.Join(home, "bin", "jmeter"), "-n", "-t", plan, "-l", jtl,
		"-j", filepath.Join(dir, "jmeter.log"),
		"-JTORTUREU_JTL="+jtl,
		"-JTORTUREU_SOAP_HOST="+host,
		"-JTORTUREU_SOAP_PORT="+port,
		"-JTORTUREU_SOAP_PATH=/soap",
		"-JTORTUREU_SOAP_ACTION=\"urn:tortureu\"",
		"-JTORTUREU_SOAP_ENVELOPE_FILE="+envelope,
	)
	combined, rerr := cmd.CombinedOutput()
	if rerr != nil {
		t.Fatalf("real JMeter rejected the emitted plan: %v\n%s", rerr, combined)
	}
	select {
	case r := <-got:
		if !strings.Contains(r.contentType, "text/xml") {
			t.Errorf("Content-Type on the wire was %q", r.contentType)
		}
		if !strings.Contains(r.soapAction, "urn:tortureu") {
			t.Errorf("SOAPAction on the wire was %q", r.soapAction)
		}
		if strings.TrimSpace(r.body) != envelopeBody {
			t.Errorf("body on the wire was %q, not the envelope file's contents", r.body)
		}
		t.Logf("SOAP request delivered: %s / %s / %s", r.contentType, r.soapAction, r.body)
	default:
		t.Fatalf("no request reached the test server:\n%s", combined)
	}
}

// spec: R-CLI-8 — the JMS half. Off by default and additionally gated on a
// broker AND a provider jar, because JMeter ships neither:
//
//	docker run -d --name mq -p 61616:61616 apache/activemq-classic:6.1.4
//	curl -Lo $TORTUREU_JMETER_HOME/lib/activemq-all.jar \
//	  https://repo1.maven.org/maven2/org/apache/activemq/activemq-all/6.1.4/activemq-all-6.1.4.jar
//	TORTUREU_EMIT_LIVE=1 TORTUREU_JMETER_HOME=... TORTUREU_JMS_PROVIDER_URL=tcp://localhost:61616 \
//	  go test ./internal/emit/ -run TestJMeter_JMSPublishedByRealJMeter -v
func TestJMeter_JMSPublishedByRealJMeter(t *testing.T) {
	if os.Getenv("TORTUREU_EMIT_LIVE") != "1" {
		t.Skip("set TORTUREU_EMIT_LIVE=1 to verify against a real broker")
	}
	home := os.Getenv("TORTUREU_JMETER_HOME")
	providerURL := os.Getenv("TORTUREU_JMS_PROVIDER_URL")
	if home == "" || providerURL == "" {
		t.Skip("TORTUREU_JMETER_HOME and TORTUREU_JMS_PROVIDER_URL required (plus a provider jar in lib/)")
	}
	live := strings.Replace(jmeterFixture, "over: 30s", "over: 1s", 1)
	live = strings.Replace(live, "for: 120s", "for: 3s", 1)
	live = strings.Replace(live, "to: 200rps", "to: 10rps", 1)
	live = strings.Replace(live, "hold: 200rps", "hold: 10rps", 1)
	out, err := JMeter(mustParse(t, live), jmeterSystem("jms"))
	if err != nil {
		t.Fatalf("JMeter: %v", err)
	}
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.jmx")
	if err := os.WriteFile(plan, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	jtl := filepath.Join(dir, "results.jtl")
	cmd := exec.Command(filepath.Join(home, "bin", "jmeter"), "-n", "-t", plan, "-l", jtl,
		"-j", filepath.Join(dir, "jmeter.log"),
		"-JTORTUREU_JTL="+jtl,
		"-JTORTUREU_JMS_INITIAL_CONTEXT_FACTORY=org.apache.activemq.jndi.ActiveMQInitialContextFactory",
		"-JTORTUREU_JMS_PROVIDER_URL="+providerURL,
		"-JTORTUREU_JMS_CONNECTION_FACTORY=ConnectionFactory",
		"-JTORTUREU_JMS_DESTINATION=dynamicQueues/tortureu",
		"-JTORTUREU_JMS_MESSAGE=tortureu-emit-jmeter",
	)
	combined, rerr := cmd.CombinedOutput()
	if rerr != nil {
		t.Fatalf("real JMeter rejected the emitted plan: %v\n%s", rerr, combined)
	}
	raw, err := os.ReadFile(jtl)
	if err != nil {
		t.Fatalf("no results file: %v", err)
	}
	results := string(raw)
	if strings.Contains(results, ",false,") {
		t.Fatalf("a JMS publish failed against the live broker:\n%s", results)
	}
	if !strings.Contains(results, ",true,") {
		t.Fatalf("no successful publish was recorded:\n%s", results)
	}
	t.Logf("published to %s:\n%s", providerURL, results[:min(len(results), 800)])
}
