package applier

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/queuefault"
)

// spec: R-EXE-17
func TestBrokerApplier_ApplyPoisonPillProducesExactlyCountMalformedRecords(t *testing.T) {
	var gotTopic, gotContentType string
	var records []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTopic = strings.TrimPrefix(r.URL.Path, "/topics/")
		gotContentType = r.Header.Get("Content-Type")
		var body struct {
			Records []map[string]any `json:"records"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		records = body.Records
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"offsets": []map[string]any{{"partition": 0, "offset": 0}}})
	}))
	defer srv.Close()

	a := &BrokerApplier{BaseURL: srv.URL}
	_, err := a.ApplyPoisonPill("f1", queuefault.PoisonPill{Topic: "orders", Count: 3})
	if err != nil {
		t.Fatalf("ApplyPoisonPill: %v", err)
	}
	if gotTopic != "orders" {
		t.Errorf("topic = %q, want %q", gotTopic, "orders")
	}
	if gotContentType != "application/vnd.kafka.binary.v2+json" {
		t.Errorf("Content-Type = %q, want the binary REST Proxy media type", gotContentType)
	}
	if len(records) != 3 {
		t.Fatalf("produced %d records, want 3 (count)", len(records))
	}
	for _, rec := range records {
		v, _ := rec["value"].(string)
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			t.Fatalf("record value is not valid base64: %v", err)
		}
		if len(raw) == 0 {
			t.Error("malformed record has an empty payload")
		}
	}
}

// spec: R-EXE-18
func TestBrokerApplier_ApplyPoisonPillUndoIsNoOp(t *testing.T) {
	// A poison pill is produced synchronously and completely before
	// ApplyPoisonPill returns, so by the time undo could run, every
	// malformed message named by Count is already durably on the topic
	// (R-EXE-18: no "un-publish"). undo must not attempt any further call.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"offsets": []map[string]any{{"partition": 0, "offset": 0}}})
	}))
	defer srv.Close()

	a := &BrokerApplier{BaseURL: srv.URL}
	undo, err := a.ApplyPoisonPill("f1", queuefault.PoisonPill{Topic: "orders", Count: 1})
	if err != nil {
		t.Fatalf("ApplyPoisonPill: %v", err)
	}
	before := calls
	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if calls != before {
		t.Errorf("undo made %d further HTTP call(s), want 0 — it cannot retract a published message", calls-before)
	}
}

// spec: R-EXE-19
func TestBrokerApplier_ApplyPoisonPillErrorsWhenBrokerRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := &BrokerApplier{BaseURL: srv.URL}
	if _, err := a.ApplyPoisonPill("f1", queuefault.PoisonPill{Topic: "orders", Count: 1}); err == nil {
		t.Fatal("ApplyPoisonPill returned nil error when the broker rejected the produce, want an error (R-EXE-19: a fault that silently fails must not report success)")
	}
}

// spec: R-CFG-23
func TestShouldDuplicate_IsDeterministicGivenARoll(t *testing.T) {
	// Pure decision logic, tested without any broker: shouldDuplicate must
	// respect Rate as a proportion (R-CFG-23), not a coin-flip with hidden
	// bias, so a fixed roll below the rate always duplicates and a fixed
	// roll at/above it never does.
	if !shouldDuplicate(0.3, 0.29) {
		t.Error("shouldDuplicate(0.3, 0.29) = false, want true (roll below rate)")
	}
	if shouldDuplicate(0.3, 0.30) {
		t.Error("shouldDuplicate(0.3, 0.30) = true, want false (roll at rate boundary)")
	}
	if shouldDuplicate(0.0, 0.0) {
		t.Error("shouldDuplicate(0.0, 0.0) = true, want false (rate 0 never duplicates)")
	}
	if !shouldDuplicate(1.0, 0.999) {
		t.Error("shouldDuplicate(1.0, 0.999) = false, want true (rate 1.0 always duplicates)")
	}
}

// spec: R-EXE-18
func TestIsDuplicateMarked_RoundTrips(t *testing.T) {
	// Re-published copies are tagged so the same re-delivery loop, which
	// also consumes its own topic, never re-duplicates a duplicate it just
	// produced (would otherwise amplify without bound — a correctness
	// requirement of an unbounded background loop, not something SPEC.md
	// states directly but necessary for R-EXE-5's teardown to ever
	// terminate a bounded number of extra messages instead of a runaway
	// one).
	original := []byte("hello")
	marked := markDuplicate(original)
	if !isDuplicateMarked(marked) {
		t.Error("isDuplicateMarked(markDuplicate(x)) = false, want true")
	}
	if isDuplicateMarked(original) {
		t.Error("isDuplicateMarked(original) = true, want false")
	}
}

// wireMockAvailable's sibling for a real broker: skips cleanly when Docker
// or the image pull is unavailable. It returns the REST Proxy base URL and
// the container id (createTopic needs the id to run `rpk topic create`
// in-container — Pandaproxy's v2 REST API this applier otherwise uses has
// no topic-creation endpoint).
func redpandaAvailable(t *testing.T) (restURL, containerID string) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	out, err := exec.Command("docker", "run", "-d",
		"-p", "0:18082",
		"redpandadata/redpanda:v24.2.7",
		"redpanda", "start", "--smp", "1", "--memory", "512M", "--overprovisioned",
		"--node-id", "0", "--check=false",
		"--kafka-addr", "internal://0.0.0.0:9092",
		"--advertise-kafka-addr", "internal://localhost:9092",
		"--pandaproxy-addr", "internal://0.0.0.0:8082,external://0.0.0.0:18082",
		"--advertise-pandaproxy-addr", "internal://localhost:8082,external://localhost:18082",
	).Output()
	if err != nil {
		t.Skipf("docker run redpanda: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })

	portOut, err := exec.Command("docker", "port", id, "18082/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	restURL = "http://" + addr

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(restURL + "/brokers")
		if err == nil {
			resp.Body.Close()
			return restURL, id
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("redpanda pandaproxy never became ready")
	return "", ""
}

// createTopic uses `rpk topic create` inside the container rather than
// Pandaproxy's REST API: the v2 Kafka REST Proxy surface this applier
// otherwise uses (produce, consumer groups) has no topic-creation endpoint,
// and auto-creation is off by default.
func createTopic(t *testing.T, containerID, topic string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := exec.Command("docker", "exec", containerID, "rpk", "topic", "create", topic).CombinedOutput()
		if err == nil {
			return
		}
		lastErr = fmt.Errorf("%w: %s", err, out)
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("could not create topic %q: %v", topic, lastErr)
}

func consumeAll(t *testing.T, restURL, topic string) []string {
	t.Helper()
	group := fmt.Sprintf("verify-%d", time.Now().UnixNano())
	body := `{"name":"c1","format":"binary","auto.offset.reset":"earliest"}`
	resp, err := http.Post(restURL+"/consumers/"+group, "application/vnd.kafka.v2+json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	resp.Body.Close()
	defer http.NewRequest(http.MethodDelete, restURL+"/consumers/"+group+"/instances/c1", nil)

	sub := fmt.Sprintf(`{"topics":["%s"]}`, topic)
	resp, err = http.Post(restURL+"/consumers/"+group+"/instances/c1/subscription", "application/vnd.kafka.v2+json", strings.NewReader(sub))
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	resp.Body.Close()
	time.Sleep(500 * time.Millisecond) // let the subscription take effect

	var values []string
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, restURL+"/consumers/"+group+"/instances/c1/records?timeout=2000", nil)
		req.Header.Set("Accept", "application/vnd.kafka.binary.v2+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("poll records: %v", err)
		}
		var recs []struct {
			Value string `json:"value"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&recs)
		resp.Body.Close()
		if len(recs) == 0 {
			continue
		}
		for _, r := range recs {
			raw, _ := base64.StdEncoding.DecodeString(r.Value)
			values = append(values, string(raw))
		}
	}
	return values
}

// spec: R-EXE-17
func TestBrokerApplier_PoisonPillAgainstRealBrokerIsActuallyOnTheTopic(t *testing.T) {
	restURL, containerID := redpandaAvailable(t)
	topic := fmt.Sprintf("poison-%d", time.Now().UnixNano())
	createTopic(t, containerID, topic)

	// Negative control: before applying anything, the topic is empty.
	if before := consumeAll(t, restURL, topic); len(before) != 0 {
		t.Fatalf("topic already had %d record(s) before ApplyPoisonPill, want 0 (bad test setup)", len(before))
	}

	a := &BrokerApplier{BaseURL: restURL}
	undo, err := a.ApplyPoisonPill("f1", queuefault.PoisonPill{Topic: topic, Count: 2})
	if err != nil {
		t.Fatalf("ApplyPoisonPill: %v", err)
	}
	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}

	got := consumeAll(t, restURL, topic)
	if len(got) != 2 {
		t.Fatalf("topic has %d record(s), want 2 (R-EXE-18: undo cannot retract them — they must still be there)", len(got))
	}
}

// spec: R-EXE-15
func TestBrokerApplier_DuplicateAgainstRealBrokerRedeliversAtRateOne(t *testing.T) {
	restURL, containerID := redpandaAvailable(t)
	topic := fmt.Sprintf("dup-%d", time.Now().UnixNano())
	createTopic(t, containerID, topic)

	a := &BrokerApplier{BaseURL: restURL}
	undo, err := a.ApplyDuplicate("f1", queuefault.Duplicate{Topic: topic, Rate: 1.0})
	if err != nil {
		t.Fatalf("ApplyDuplicate: %v", err)
	}
	t.Cleanup(func() { undo() })

	// Produce one distinct message directly (simulating the SUT's own
	// traffic) while the re-delivery loop runs against the same topic.
	marker := fmt.Sprintf("payload-%d", time.Now().UnixNano())
	produceOne(t, restURL, topic, marker)

	// Give the background loop a bounded window to poll, see the marker,
	// and re-publish its duplicate before taking one single, final
	// snapshot of the whole topic. Counting must come from exactly ONE
	// from-earliest read: consumeAll's "verify-<nanotime>" consumer group
	// always starts at the earliest offset, so calling it repeatedly in a
	// loop would recount the very same on-topic message every iteration
	// and pass whether or not any duplicate was ever produced — the
	// tautology this project's tests must not become. Sleeping first and
	// reading once means "found it twice" can only be true if the topic
	// itself holds two copies.
	time.Sleep(3 * time.Second)

	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}

	// A re-published copy carries duplicatePrefix ahead of the original
	// payload (see markDuplicate) so the loop's own consumption of it
	// never re-duplicates it again — strings.Contains, not ==, is what a
	// real downstream consumer would see too: the same business payload,
	// delivered a second time.
	//
	// spec: R-EXE-23
	// The count MUST be exact, not a lower bound: R-EXE-23 exists because
	// a loop that consumes and republishes on the same topic can consume
	// its own republished copy and duplicate it again, compounding without
	// bound. A ">= 2" assertion cannot distinguish "duplicated exactly
	// once, as the guard requires" from "duplicated repeatedly because the
	// guard was deleted" — both satisfy >= 2. Only "== 2" fails when the
	// guard is missing.
	occurrences := 0
	for _, v := range consumeAll(t, restURL, topic) {
		if strings.Contains(v, marker) {
			occurrences++
		}
	}
	if occurrences != 2 {
		t.Fatalf("marker payload appears %d time(s) on the topic, want exactly 2 (original + one re-delivery at rate 1.0; R-EXE-23's guard must stop it there, not let it compound)", occurrences)
	}
}

func produceOne(t *testing.T, restURL, topic, value string) {
	t.Helper()
	enc := base64.StdEncoding.EncodeToString([]byte(value))
	body := fmt.Sprintf(`{"records":[{"value":"%s"}]}`, enc)
	resp, err := http.Post(restURL+"/topics/"+topic, "application/vnd.kafka.binary.v2+json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("produce: %v", err)
	}
	resp.Body.Close()
}

// spec: R-EXE-5
func TestBrokerApplier_UndoStopsFurtherDuplicationBeforeNextCopy(t *testing.T) {
	// Without a live broker: undo must halt the loop's goroutine
	// deterministically (no more HTTP calls after undo returns), which is
	// what lets Manager.Teardown (R-EXE-5) run safely from a deferred
	// recover without leaking a goroutine that keeps calling out.
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		switch {
		case strings.HasPrefix(r.URL.Path, "/consumers/") && strings.HasSuffix(r.URL.Path, "/records"):
			_ = json.NewEncoder(w).Encode([]any{})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"instance_id": "c1", "base_uri": "unused"})
		}
	}))
	defer srv.Close()

	a := &BrokerApplier{BaseURL: srv.URL, pollInterval: 10 * time.Millisecond}
	undo, err := a.ApplyDuplicate("f1", queuefault.Duplicate{Topic: "orders", Rate: 0.5})
	if err != nil {
		t.Fatalf("ApplyDuplicate: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	mu.Lock()
	after := calls
	mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	settled := calls
	mu.Unlock()
	if settled != after {
		t.Errorf("calls kept growing after undo returned (%d -> %d): the loop was not actually stopped", after, settled)
	}
}
