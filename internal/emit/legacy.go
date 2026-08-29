// Apache JMeter — load over the protocols k6 does not speak
// (registry.yaml: tier delegate, when: dep:jms|dep:ldap|dep:soap, how:
// "tortureu emit jmeter", note "only for protocols k6 lacks"). R-CLI-8
// proposed.
//
// The registry's note is the whole scope. k6 drives HTTP, gRPC, WebSocket
// and (with xk6-kafka) Kafka; TortureU already emits those. JMS, LDAP and
// SOAP are the three R-DET-9 types with no k6 path at all, so this emits a
// .jmx test plan for exactly those and for nothing else. It is not a second
// HTTP load generator.
//
// VERIFICATION STATUS (what was actually run on this host, 2026-08-09):
//
//   - GROUND TRUTH, not memory: every element name, guiclass, testclass and
//     property name below was produced by JMeter 5.6.3's OWN SaveService.
//     A Java program (JDK 17, against the distribution's jars) constructed
//     PublisherSampler, LDAPExtSampler, HTTPSamplerProxy, HeaderManager,
//     ThreadGroup and PreciseThroughputTimer through their real setters and
//     serialised the tree; this file emits that serialisation's shape. The
//     property constants were separately dumped by reflection over
//     BaseJMSSampler/PublisherSampler/LDAPExtSampler.
//   - VERIFIED end to end for LDAP: the emitted plan was run by a real
//     `jmeter -n` (Apache JMeter 5.6.3, OpenJDK 25) against a real directory
//     server (bitnami/openldap in Docker). Its bind, search and unbind
//     samples all succeeded and were written to a .jtl. So the LDAPExtSampler
//     properties, the ${__P(...)} indirection and the scheduler settings are
//     confirmed against a real server, not just a real parser.
//   - VERIFIED end to end for SOAP: the same plan, run against a local HTTP
//     server, delivered the POST with Content-Type: text/xml; charset=utf-8,
//     the SOAPAction header, and the envelope body read from the file the
//     TORTUREU_SOAP_ENVELOPE_FILE property names.
//   - VERIFIED end to end for JMS: against a real broker
//     (apache/activemq-classic:5.18.6) with activemq-all-5.18.6.jar dropped
//     into JMeter's lib/, the emitted PublisherSampler published real
//     messages — "200, 1 messages published", zero failures — to
//     dynamicQueues/tortureu over tcp://.
//   - VERIFIED, the hard way, and worth keeping: the SAME plan against
//     apache/activemq-classic:6.1.4 with activemq-all-6.1.4.jar fails every
//     sample with "javax.naming.NamingException: Expected
//     javax.jms.ConnectionFactory, found
//     org.apache.activemq.ActiveMQConnectionFactory". JMeter 5.6.3 ships the
//     JAVAX JMS API (geronimo-jms_1.1_spec); ActiveMQ 6 moved to
//     jakarta.jms, so its factory does not implement the interface JMeter
//     looks up. That is a provider-jar constraint no amount of correct
//     property names avoids, and the emitted plan says so.
//   - NOT VERIFIED: the JMS point-to-point sampler (JMSSampler) is not
//     emitted at all — see below. Nor was any JMS destination consumed:
//     what a published message costs a real consumer is that consumer's
//     property, not this plan's.
//
// What it refuses to invent:
//
//   - Every endpoint. jms, ldap and soap are lockfile-only dependencies
//     (R-DET-13): finding `spring-jms` in a manifest proves this service
//     SPEAKS JMS and says nothing about which broker, at which URL, with
//     which connection factory. There is no address to read, so every one is
//     a ${__P(...)} JMeter property whose default is a loud
//     TORTUREU-UNSET-<NAME> marker: an unset plan fails naming the property
//     it needs, instead of quietly connecting to a default that happens to
//     resolve.
//   - The message and the envelope. A JMS body and a SOAP envelope are
//     application payloads; torture.yaml has no field for either. The JMS
//     message defaults to the unset marker, and the SOAP envelope is read
//     from a file the operator names — inventing an envelope means sending a
//     document some real service will try to process.
//   - The point-to-point JMS shape. JMeter has two JMS samplers with no
//     property names in common: PublisherSampler (jms.*) and JMSSampler
//     (JMSSampler.*, with its message body under the misspelled
//     `HTTPSamper.xml_data`). Which one a repo needs depends on whether it
//     publishes or does request/reply, and a `javax.jms` dependency does not
//     say. This emits the publisher and says the other exists, rather than
//     emitting both and letting the operator discover that half the plan
//     talks to a queue nobody meant to touch.
//   - Any fault. JMeter here is a load generator; every fault is reported as
//     untranslated inside the plan (R-CLI-8).
//
// The load model, stated because it is a real mismatch: torture.yaml's model
// is arrival_rate (R-CFG-6), which is open; a JMeter ThreadGroup is closed.
// The translation uses PreciseThroughputTimer — JMeter's own Poisson-arrival
// timer, built in since 4.0 and not a plugin — at load.stages' peak rate,
// with the thread count sized as an upper bound on concurrency. That is as
// close as JMeter core gets, and it is not the same thing: if the dependency
// slows past what the threads can cover, the offered rate falls, where a true
// open model's would not.
package emit

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/detect"
	"github.com/jd316/TortureU/internal/fault"
)

// jmeterVersion is the JMeter this file's ground truth came from, and the
// version stamped into the plan's own header attributes.
const jmeterVersion = "5.6.3"

// jmeterPropsVersion is the saveservice format version JMeter 5.6.3 itself
// writes (bin/saveservice.properties: _version=5.0).
const jmeterPropsVersion = "5.0"

// jmeterProtocol describes one protocol this emitter covers: the R-DET-9
// dependency type that triggers it, and the human name used in comments.
type jmeterProtocol struct {
	depType string
	label   string
}

var jmeterProtocols = []jmeterProtocol{
	{depType: "ldap", label: "LDAP"},
	{depType: "jms", label: "JMS"},
	{depType: "soap", label: "SOAP"},
}

// jmeterProp renders a JMeter property reference with a self-describing
// default, so an unset property produces "TORTUREU-UNSET-<NAME>" in the
// failure message rather than an empty string or a silent default.
func jmeterProp(name string) string {
	return "${__P(" + name + ",TORTUREU-UNSET-" + name + ")}"
}

// jmeterEsc escapes a value for XML text content, using the standard
// library rather than hand-rolled replacement: torture.yaml fault names and
// SQL-ish assert text are free-form and land in this document.
func jmeterEsc(s string) string {
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(s)); err != nil {
		return ""
	}
	return buf.String()
}

// jmeterComment renders an XML comment, with the one sequence XML comments
// cannot contain ("--") defused. JMeter's parser skips comments, so this is
// how R-CLI-8's per-fault reporting reaches a document with no comment
// syntax of its own.
func jmeterComment(body string) string {
	safe := strings.ReplaceAll(body, "--", "- -")
	return "<!--\n" + safe + "-->\n"
}

// JMeter emits a .jmx test plan driving the detected JMS/LDAP/SOAP
// dependencies, as described in this file's header.
func JMeter(cfg *config.Config, sys *detect.System) (string, error) {
	if sys == nil {
		return "<!-- tortureu emit jmeter: the system could not be detected, so whether this repo\n" +
			"     speaks JMS, LDAP or SOAP is unknown — which is not the same as knowing it speaks\n" +
			"     none of them; nothing to emit. -->\n", nil
	}
	var present []jmeterProtocol
	for _, p := range jmeterProtocols {
		if _, ok := findDep(sys, p.depType); ok {
			present = append(present, p)
		}
	}
	if len(present) == 0 {
		return "<!-- tortureu emit jmeter: no jms, ldap or soap dependency was detected\n" +
			"     (dep:jms|dep:ldap|dep:soap). JMeter is emitted here only for the protocols k6\n" +
			"     cannot speak; everything else this repo talks to already has a k6 path\n" +
			"     (tortureu emit k6-load / kafka-load / grpc). Nothing to emit. -->\n", nil
	}

	steps, err := buildSteps(cfg)
	if err != nil {
		return "", fmt.Errorf("emit jmeter: %w", err)
	}
	rate, hasRate := peakRPS(cfg.Load)
	total, hasTotal := totalSeconds(cfg.Load)
	threads := 10
	if hasRate {
		threads = concurrencyFromRPS(rate)
	}
	ramp := 0
	for _, s := range steps {
		if s.ramp {
			ramp = int(s.secs)
			break
		}
	}

	var faultNotes strings.Builder
	for _, f := range cfg.Faults {
		if _, terr := fault.Translate(f); terr != nil {
			return "", fmt.Errorf("emit jmeter: %w", terr)
		}
		faultNotes.WriteString(atComment(f))
		faultNotes.WriteString(skipComment("jmeter", f,
			"this plan is a load generator for protocols k6 lacks; it injects nothing. Use \"tortureu emit pumba\", \"netem\" or \"iptables\""))
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(jmeterComment(jmeterPlanHeader(present, hasRate, rate, hasTotal, total, threads)))
	fmt.Fprintf(&b, `<jmeterTestPlan version="1.2" properties="%s" jmeter="%s">`+"\n", jmeterPropsVersion, jmeterVersion)
	b.WriteString("  <hashTree>\n")
	b.WriteString(`    <TestPlan guiclass="TestPlanGui" testclass="TestPlan" testname="tortureu" enabled="true">` + "\n")
	b.WriteString(`      <boolProp name="TestPlan.functional_mode">false</boolProp>` + "\n")
	b.WriteString(`      <boolProp name="TestPlan.serialize_threadgroups">false</boolProp>` + "\n")
	b.WriteString(`      <elementProp name="TestPlan.user_defined_variables" elementType="Arguments" guiclass="ArgumentsPanel" testclass="Arguments" testname="User Defined Variables" enabled="true">` + "\n")
	b.WriteString(`        <collectionProp name="Arguments.arguments"/>` + "\n")
	b.WriteString("      </elementProp>\n")
	b.WriteString("    </TestPlan>\n")
	b.WriteString("    <hashTree>\n")

	// ThreadGroup. on_sample_error is "continue" on purpose: a failing
	// sample is the measurement, not a reason to stop measuring.
	b.WriteString(`      <ThreadGroup guiclass="ThreadGroupGui" testclass="ThreadGroup" testname="tortureu-load" enabled="true">` + "\n")
	b.WriteString(`        <stringProp name="ThreadGroup.on_sample_error">continue</stringProp>` + "\n")
	b.WriteString(`        <elementProp name="ThreadGroup.main_controller" elementType="LoopController" guiclass="LoopControlPanel" testclass="LoopController" testname="Loop Controller" enabled="true">` + "\n")
	b.WriteString(`          <boolProp name="LoopController.continue_forever">false</boolProp>` + "\n")
	b.WriteString(`          <stringProp name="LoopController.loops">-1</stringProp>` + "\n")
	b.WriteString("        </elementProp>\n")
	fmt.Fprintf(&b, `        <stringProp name="ThreadGroup.num_threads">%d</stringProp>`+"\n", threads)
	fmt.Fprintf(&b, `        <stringProp name="ThreadGroup.ramp_time">%d</stringProp>`+"\n", ramp)
	if hasTotal {
		b.WriteString(`        <boolProp name="ThreadGroup.scheduler">true</boolProp>` + "\n")
		fmt.Fprintf(&b, `        <stringProp name="ThreadGroup.duration">%d</stringProp>`+"\n", total)
		b.WriteString(`        <stringProp name="ThreadGroup.delay">0</stringProp>` + "\n")
	} else {
		// No stage carries a duration, so nothing here may claim one:
		// loops:-1 without a scheduler runs until stopped, which is visibly
		// unbounded rather than silently five minutes long.
		b.WriteString(`        <boolProp name="ThreadGroup.scheduler">false</boolProp>` + "\n")
	}
	b.WriteString("      </ThreadGroup>\n")
	b.WriteString("      <hashTree>\n")

	if hasRate {
		b.WriteString(jmeterThroughputTimer(rate, total, hasTotal))
	} else {
		b.WriteString(jmeterComment("        No load.stages rate in torture.yaml, so no throughput timer is emitted.\n" +
			"        The threads below will sample as fast as the dependency answers, which is a\n" +
			"        firehose rather than a load profile. Declare load.stages, or add a timer.\n"))
	}

	for _, p := range present {
		switch p.depType {
		case "ldap":
			b.WriteString(jmeterLDAPSamplers())
		case "jms":
			b.WriteString(jmeterJMSSampler())
		case "soap":
			b.WriteString(jmeterSOAPSampler())
		}
	}

	b.WriteString("      </hashTree>\n")
	b.WriteString(jmeterResultCollector())
	b.WriteString("    </hashTree>\n")

	if faultNotes.Len() > 0 {
		b.WriteString(jmeterComment("    faults declared in torture.yaml that this emit does NOT translate\n" +
			"    (listed, never dropped — R-CLI-8):\n\n" + jmeterEsc(faultNotes.String())))
	}
	b.WriteString("  </hashTree>\n")
	b.WriteString("</jmeterTestPlan>\n")
	return b.String(), nil
}

// jmeterPlanHeader is the comment a human sees first when they open the
// plan: what to set, what is not verified, and what this plan will not do.
func jmeterPlanHeader(present []jmeterProtocol, hasRate bool, rate int, hasTotal bool, total, threads int) string {
	var b strings.Builder
	labels := make([]string, 0, len(present))
	for _, p := range present {
		labels = append(labels, p.label)
	}
	b.WriteString("  Generated by tortureu emit jmeter (Apache JMeter " + jmeterVersion + ").\n\n")
	b.WriteString("    jmeter -n -t this-plan.jmx -l results.jtl \\\n")
	b.WriteString("           -JTORTUREU_<NAME>=<value>   (see the properties below)\n\n")
	b.WriteString("  Protocols in this plan, and ONLY these: " + strings.Join(labels, ", ") + ".\n")
	b.WriteString("  They are here because detection found a client for each in a manifest\n" +
		"  (R-DET-13). That is all it found: a lockfile entry proves this service SPEAKS\n" +
		"  the protocol and says nothing about which server, at which address. So every\n" +
		"  endpoint below is a JMeter property with a TORTUREU-UNSET-<NAME> default. An\n" +
		"  unset plan fails naming the property it wanted; it does not quietly connect to\n" +
		"  a default that happens to resolve.\n\n")
	if hasRate {
		fmt.Fprintf(&b, "  Load: %d rps (load.stages' peak) offered through a PreciseThroughputTimer,\n", rate)
		fmt.Fprintf(&b, "  with %d threads. torture.yaml's model is arrival_rate, which is OPEN\n", threads)
		b.WriteString("  (R-CFG-6); a JMeter ThreadGroup is closed. The timer is JMeter's own\n" +
			"  Poisson-arrival timer and is the closest core JMeter gets, but the two are not\n" +
			"  the same: if the server slows past what these threads can cover, the offered\n" +
			"  rate falls, where a true open model's would not. That is a real difference in\n" +
			"  what this measures, not a rounding detail.\n")
	}
	if hasTotal {
		fmt.Fprintf(&b, "  Duration: %ds, the sum of load.stages' over:/for: windows.\n", total)
	}
	b.WriteString("\n  NOT SCHEDULED: torture.yaml's faults are listed at the end of this file as\n" +
		"  untranslated. This plan injects nothing and anchors nothing to a phase clock —\n" +
		"  that is what delegate tier means (real output, separate timing).\n")
	for _, p := range present {
		switch p.depType {
		case "jms":
			b.WriteString("\n  JMS — READ THIS: JMeter ships the JMS API jar only\n" +
				"  (geronimo-jms_1.1_spec, i.e. JAVAX jms). It has NO provider. Drop your broker's\n" +
				"  client jar into JMeter's lib/ (e.g. activemq-all) before running this.\n" +
				"  It must be a JAVAX-JMS provider: verified working with activemq-all 5.18.6, and\n" +
				"  verified FAILING with activemq-all 6.1.4, which is jakarta.jms and makes every\n" +
				"  sample die on \"Expected javax.jms.ConnectionFactory\".\n" +
				"  This is the JMS Publisher. JMeter's other JMS sampler (JMSSampler, request/\n" +
				"  reply point-to-point) shares no property names with it; a javax.jms dependency\n" +
				"  does not say which one you need, so only the publisher is emitted.\n")
		case "soap":
			b.WriteString("\n  SOAP: JMeter REMOVED its SOAP/XML-RPC sampler in 3.2 (Bug 60727); its\n" +
				"  documented replacement is an HTTP Request with a raw body, which is what is\n" +
				"  below. The envelope is read from the file TORTUREU_SOAP_ENVELOPE_FILE names —\n" +
				"  torture.yaml carries no payload, and an invented envelope is a document a real\n" +
				"  service would try to process.\n")
		}
	}
	return b.String()
}

// jmeterThroughputTimer renders PreciseThroughputTimer. It is a TestBean, so
// its properties are named after the bean properties and the doubles are
// doubleProp elements — the shape JMeter 5.6.3's SaveService itself wrote for
// a timer built through its setters.
func jmeterThroughputTimer(rate, total int, hasTotal bool) string {
	duration := total
	if !hasTotal {
		// Its `duration` is the length of the schedule the timer plans
		// arrivals over. With no stage duration to read, there is nothing
		// honest to put here, so the timer is emitted with the same
		// unbounded intent as the thread group: a very long horizon rather
		// than a made-up window that would silently stop offering load.
		duration = 0
	}
	var b strings.Builder
	b.WriteString(`        <PreciseThroughputTimer guiclass="TestBeanGUI" testclass="PreciseThroughputTimer" testname="tortureu-rate" enabled="true">` + "\n")
	b.WriteString("          <doubleProp>\n")
	b.WriteString("            <name>throughput</name>\n")
	fmt.Fprintf(&b, "            <value>%d.0</value>\n", rate)
	b.WriteString("            <savedValue>0.0</savedValue>\n")
	b.WriteString("          </doubleProp>\n")
	b.WriteString(`          <intProp name="throughputPeriod">1</intProp>` + "\n")
	fmt.Fprintf(&b, `          <longProp name="duration">%d</longProp>`+"\n", duration)
	b.WriteString(`          <intProp name="batchSize">1</intProp>` + "\n")
	b.WriteString(`          <intProp name="batchThreadDelay">0</intProp>` + "\n")
	b.WriteString("        </PreciseThroughputTimer>\n")
	b.WriteString("        <hashTree/>\n")
	return b.String()
}

// jmeterLDAPSamplers renders bind -> search -> unbind. All three are needed:
// a search on an unbound connection fails on any directory that disallows
// anonymous access, and leaving the connection bound leaks it across
// iterations.
func jmeterLDAPSamplers() string {
	var b strings.Builder
	b.WriteString(jmeterComment("        LDAP. Properties: TORTUREU_LDAP_HOST, _PORT, _ROOTDN, _BIND_DN, _BIND_PW,\n" +
		"        _SEARCH_BASE, _SEARCH_FILTER. scope 2 = subtree (0 object, 1 onelevel).\n" +
		"        Set TORTUREU_LDAP_SECURE=true for LDAPS; it is false here because a plaintext\n" +
		"        default that silently works is better than a TLS default that silently does not.\n"))
	b.WriteString(jmeterLDAPSampler("bind", "1. bind", map[string]string{
		"user_dn": jmeterProp("TORTUREU_LDAP_BIND_DN"),
		"user_pw": jmeterProp("TORTUREU_LDAP_BIND_PW"),
	}))
	b.WriteString(jmeterLDAPSampler("search", "2. search", map[string]string{
		"search":       jmeterProp("TORTUREU_LDAP_SEARCH_BASE"),
		"searchfilter": jmeterProp("TORTUREU_LDAP_SEARCH_FILTER"),
		"scope":        "2",
		"countlimit":   "0",
		"timelimit":    "0",
	}))
	b.WriteString(jmeterLDAPSampler("unbind", "3. unbind", nil))
	return b.String()
}

// jmeterLDAPSampler renders one LDAPExtSampler. The property names are bare
// and unprefixed (servername, rootdn, user_dn, ...) — that is genuinely how
// this element is stored, confirmed against JMeter 5.6.3's serialisation.
func jmeterLDAPSampler(test, name string, extra map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `        <LDAPExtSampler guiclass="LdapExtTestSamplerGui" testclass="LDAPExtSampler" testname="%s" enabled="true">`+"\n", jmeterEsc(name))
	fmt.Fprintf(&b, `          <stringProp name="servername">%s</stringProp>`+"\n", jmeterProp("TORTUREU_LDAP_HOST"))
	fmt.Fprintf(&b, `          <stringProp name="port">%s</stringProp>`+"\n", jmeterProp("TORTUREU_LDAP_PORT"))
	fmt.Fprintf(&b, `          <stringProp name="rootdn">%s</stringProp>`+"\n", jmeterProp("TORTUREU_LDAP_ROOTDN"))
	b.WriteString(`          <stringProp name="secure">${__P(TORTUREU_LDAP_SECURE,false)}</stringProp>` + "\n")
	fmt.Fprintf(&b, `          <stringProp name="test">%s</stringProp>`+"\n", test)
	for _, key := range []string{"user_dn", "user_pw", "search", "searchfilter", "scope", "countlimit", "timelimit"} {
		if v, ok := extra[key]; ok {
			fmt.Fprintf(&b, `          <stringProp name="%s">%s</stringProp>`+"\n", key, v)
		}
	}
	b.WriteString("        </LDAPExtSampler>\n")
	b.WriteString("        <hashTree/>\n")
	return b.String()
}

// jmeterJMSSampler renders the JMS Publisher. jms.topic is the destination
// name for queues as well as topics (BaseJMSSampler's DEST constant is
// literally "jms.topic"), which is why the property is named DESTINATION.
func jmeterJMSSampler() string {
	var b strings.Builder
	b.WriteString(jmeterComment("        JMS Publisher. Properties: TORTUREU_JMS_INITIAL_CONTEXT_FACTORY,\n" +
		"        _PROVIDER_URL, _CONNECTION_FACTORY, _DESTINATION, _MESSAGE.\n" +
		"        jms.topic below is the DESTINATION field for queues too — that is JMeter's own\n" +
		"        property name for it, not a topic-only setting.\n" +
		"        Requires your broker's provider jar in JMeter's lib/ (e.g. activemq-all): the\n" +
		"        distribution ships the JMS API only. NOT verified against a real broker.\n"))
	b.WriteString(`        <PublisherSampler guiclass="JMSPublisherGui" testclass="PublisherSampler" testname="jms-publish" enabled="true">` + "\n")
	b.WriteString(`          <stringProp name="jms.jndi_properties">false</stringProp>` + "\n")
	fmt.Fprintf(&b, `          <stringProp name="jms.initial_context_factory">%s</stringProp>`+"\n", jmeterProp("TORTUREU_JMS_INITIAL_CONTEXT_FACTORY"))
	fmt.Fprintf(&b, `          <stringProp name="jms.provider_url">%s</stringProp>`+"\n", jmeterProp("TORTUREU_JMS_PROVIDER_URL"))
	fmt.Fprintf(&b, `          <stringProp name="jms.connection_factory">%s</stringProp>`+"\n", jmeterProp("TORTUREU_JMS_CONNECTION_FACTORY"))
	fmt.Fprintf(&b, `          <stringProp name="jms.topic">%s</stringProp>`+"\n", jmeterProp("TORTUREU_JMS_DESTINATION"))
	b.WriteString(`          <stringProp name="jms.iterations">1</stringProp>` + "\n")
	b.WriteString(`          <boolProp name="jms.authenticate">false</boolProp>` + "\n")
	b.WriteString(`          <stringProp name="jms.config_choice">jms_use_text</stringProp>` + "\n")
	b.WriteString(`          <stringProp name="jms.config_msg_type">jms_text_message</stringProp>` + "\n")
	fmt.Fprintf(&b, `          <stringProp name="jms.text_message">%s</stringProp>`+"\n", jmeterProp("TORTUREU_JMS_MESSAGE"))
	b.WriteString("        </PublisherSampler>\n")
	b.WriteString("        <hashTree/>\n")
	return b.String()
}

// jmeterSOAPSampler renders the HTTP Request that replaced JMeter's removed
// SOAP sampler, plus the HeaderManager scoped to it (a HeaderManager is
// scoped by being a CHILD of the sampler, so it lives inside the sampler's
// own hashTree).
func jmeterSOAPSampler() string {
	var b strings.Builder
	b.WriteString(jmeterComment("        SOAP over HTTP. Properties: TORTUREU_SOAP_HOST, _PORT, _PROTOCOL, _PATH,\n" +
		"        _ACTION, _ENVELOPE_FILE. JMeter removed its SOAP/XML-RPC sampler in 3.2\n" +
		"        (Bug 60727); this is the documented replacement.\n" +
		"        The envelope is read from a FILE at run time via __FileToString, because\n" +
		"        torture.yaml carries no payload and an invented envelope is a real document a\n" +
		"        real service would act on.\n"))
	b.WriteString(`        <HTTPSamplerProxy guiclass="HttpTestSampleGui" testclass="HTTPSamplerProxy" testname="soap-request" enabled="true">` + "\n")
	b.WriteString(`          <boolProp name="HTTPSampler.postBodyRaw">true</boolProp>` + "\n")
	b.WriteString(`          <elementProp name="HTTPsampler.Arguments" elementType="Arguments">` + "\n")
	b.WriteString(`            <collectionProp name="Arguments.arguments">` + "\n")
	b.WriteString(`              <elementProp name="" elementType="HTTPArgument">` + "\n")
	b.WriteString(`                <boolProp name="HTTPArgument.always_encode">false</boolProp>` + "\n")
	fmt.Fprintf(&b, `                <stringProp name="Argument.value">${__FileToString(%s,,)}</stringProp>`+"\n", jmeterProp("TORTUREU_SOAP_ENVELOPE_FILE"))
	b.WriteString(`                <stringProp name="Argument.metadata">=</stringProp>` + "\n")
	b.WriteString("              </elementProp>\n")
	b.WriteString("            </collectionProp>\n")
	b.WriteString("          </elementProp>\n")
	fmt.Fprintf(&b, `          <stringProp name="HTTPSampler.domain">%s</stringProp>`+"\n", jmeterProp("TORTUREU_SOAP_HOST"))
	fmt.Fprintf(&b, `          <stringProp name="HTTPSampler.port">%s</stringProp>`+"\n", jmeterProp("TORTUREU_SOAP_PORT"))
	b.WriteString(`          <stringProp name="HTTPSampler.protocol">${__P(TORTUREU_SOAP_PROTOCOL,http)}</stringProp>` + "\n")
	fmt.Fprintf(&b, `          <stringProp name="HTTPSampler.path">%s</stringProp>`+"\n", jmeterProp("TORTUREU_SOAP_PATH"))
	b.WriteString(`          <stringProp name="HTTPSampler.method">POST</stringProp>` + "\n")
	b.WriteString(`          <boolProp name="HTTPSampler.follow_redirects">true</boolProp>` + "\n")
	b.WriteString(`          <boolProp name="HTTPSampler.use_keepalive">true</boolProp>` + "\n")
	b.WriteString("        </HTTPSamplerProxy>\n")
	b.WriteString("        <hashTree>\n")
	b.WriteString(`          <HeaderManager guiclass="HeaderPanel" testclass="HeaderManager" testname="soap-headers" enabled="true">` + "\n")
	b.WriteString(`            <collectionProp name="HeaderManager.headers">` + "\n")
	b.WriteString(`              <elementProp name="" elementType="Header">` + "\n")
	b.WriteString(`                <stringProp name="Header.name">Content-Type</stringProp>` + "\n")
	b.WriteString(`                <stringProp name="Header.value">text/xml; charset=utf-8</stringProp>` + "\n")
	b.WriteString("              </elementProp>\n")
	b.WriteString(`              <elementProp name="" elementType="Header">` + "\n")
	b.WriteString(`                <stringProp name="Header.name">SOAPAction</stringProp>` + "\n")
	fmt.Fprintf(&b, `                <stringProp name="Header.value">%s</stringProp>`+"\n", jmeterProp("TORTUREU_SOAP_ACTION"))
	b.WriteString("              </elementProp>\n")
	b.WriteString("            </collectionProp>\n")
	b.WriteString("          </HeaderManager>\n")
	b.WriteString("          <hashTree/>\n")
	b.WriteString("        </hashTree>\n")
	return b.String()
}

// jmeterResultCollector writes the .jtl. Its filename is a property so the
// plan does not decide where to write in someone else's working directory;
// `jmeter -l` overrides it anyway, which is the ordinary way to run this.
func jmeterResultCollector() string {
	var b strings.Builder
	b.WriteString(`      <ResultCollector guiclass="SimpleDataWriter" testclass="ResultCollector" testname="tortureu-results" enabled="true">` + "\n")
	b.WriteString(`        <boolProp name="ResultCollector.error_logging">false</boolProp>` + "\n")
	b.WriteString("        <objProp>\n")
	b.WriteString("          <name>saveConfig</name>\n")
	b.WriteString(`          <value class="SampleSaveConfiguration">` + "\n")
	for _, f := range []string{"time", "latency", "timestamp", "success", "label", "code", "message",
		"threadName", "dataType", "assertions", "subresults", "fieldNames", "bytes", "sentBytes",
		"url", "threadCounts", "idleTime", "connectTime"} {
		fmt.Fprintf(&b, "            <%s>true</%s>\n", f, f)
	}
	for _, f := range []string{"encoding", "responseData", "samplerData", "xml", "responseHeaders",
		"requestHeaders", "responseDataOnError"} {
		fmt.Fprintf(&b, "            <%s>false</%s>\n", f, f)
	}
	b.WriteString("            <saveAssertionResultsFailureMessage>true</saveAssertionResultsFailureMessage>\n")
	b.WriteString("            <assertionsResultsToSave>0</assertionsResultsToSave>\n")
	b.WriteString("          </value>\n")
	b.WriteString("        </objProp>\n")
	fmt.Fprintf(&b, `        <stringProp name="filename">%s</stringProp>`+"\n", "${__P(TORTUREU_JTL,tortureu-results.jtl)}")
	b.WriteString("      </ResultCollector>\n")
	b.WriteString("      <hashTree/>\n")
	return b.String()
}

func init() {
	// needsSystem: true. Which of jms/ldap/soap to emit is a detection fact
	// (R-DET-13, lockfile-only types), and this emitter must be able to say
	// "detection did not run" rather than "you speak none of these".
	Register("jmeter", JMeter, true)
}
