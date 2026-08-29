package applier

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/jd316/TortureU/internal/queuefault"
)

// poisonPillMarker is the payload every poison_pill record carries. It is
// deliberately not valid JSON/Avro/protobuf for any real schema — the
// point of a poison pill (RESEARCH.md §18) is a message a consumer's
// deserializer chokes on, not merely an unexpected-but-parseable value.
const poisonPillMarker = "\x00TORTUREU_POISON_PILL_MALFORMED\xffnot-valid-per-any-schema\x00"

// duplicatePrefix tags a record ApplyDuplicate re-published so its own
// consume loop never re-duplicates a duplicate it just produced. Without
// this, a loop consuming and re-publishing on the SAME topic would amplify
// without bound; SPEC.md does not name this mechanism (it is an
// implementation necessity of running the re-delivery loop at all, not a
// modifier of the fault's observable rate), but it must exist for the loop
// to terminate the amount of extra traffic it creates.
const duplicatePrefix = "\x00TORTUREU_DUPLICATE\x00"

func markDuplicate(value []byte) []byte {
	return append([]byte(duplicatePrefix), value...)
}

func isDuplicateMarked(value []byte) bool {
	return bytes.HasPrefix(value, []byte(duplicatePrefix))
}

// shouldDuplicate reports whether a message drawn with roll (a value in
// [0,1)) should be re-delivered under rate (R-CFG-23: rate is a proportion,
// 0.0..1.0). Extracted as a pure function so the decision is testable
// without a broker or a real random source.
func shouldDuplicate(rate, roll float64) bool {
	return roll < rate
}

// BrokerApplier is the real implementation of queuefault.Applier
// (poison_pill and duplicate, R-EXE-15's broker-producer row). It drives a
// Kafka REST Proxy v2-compatible HTTP endpoint (Confluent REST Proxy;
// Redpanda's Pandaproxy implements the same API) rather than a Kafka
// wire-protocol client library — no new module dependency, and net/http is
// already used the same way by ToxiproxyApplier and WireMockApplier.
type BrokerApplier struct {
	// BaseURL is the REST Proxy's address, e.g. "http://localhost:8082".
	BaseURL string
	Client  *http.Client

	// pollInterval overrides the duplicate loop's poll cadence; defaults to
	// 500ms. Exists so tests don't wait real-world seconds for a poll.
	pollInterval time.Duration
}

func (a *BrokerApplier) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

func (a *BrokerApplier) poll() time.Duration {
	if a.pollInterval > 0 {
		return a.pollInterval
	}
	return 500 * time.Millisecond
}

func (a *BrokerApplier) post(path, contentType string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodPost, a.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, err
}

// ApplyPoisonPill produces p.Count malformed records onto p.Topic, all
// before returning. The returned undo is always a no-op: R-EXE-18 — a
// produced record is durably in the topic's log the instant the broker
// acknowledges it, and this call always waits for every record to be
// acknowledged before returning, so there is never an in-flight batch left
// for undo to stop.
func (a *BrokerApplier) ApplyPoisonPill(name string, p queuefault.PoisonPill) (func() error, error) {
	records := make([]map[string]string, p.Count)
	for i := range records {
		records[i] = map[string]string{
			"value": base64.StdEncoding.EncodeToString([]byte(poisonPillMarker)),
		}
	}
	body, err := json.Marshal(map[string]any{"records": records})
	if err != nil {
		return nil, err
	}
	respBody, status, err := a.post("/topics/"+p.Topic, "application/vnd.kafka.binary.v2+json", body)
	if err != nil {
		return nil, fmt.Errorf("applier: broker: fault %q: produce poison_pill: %w", name, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("applier: broker: fault %q: produce poison_pill: status %d: %s", name, status, respBody)
	}
	return func() error { return nil }, nil
}

// ApplyDuplicate starts a background loop that consumes d.Topic (via a
// dedicated consumer group unique to this fault) and re-publishes a d.Rate
// fraction of the records it reads back onto the same topic, so the SUT's
// own consumers see some fraction of messages delivered twice. The
// returned undo cancels the loop and waits for its current iteration to
// finish before returning, so no further copy is produced after undo
// returns (R-EXE-5) — but it does not retract any duplicate already
// produced or consumed (R-EXE-18, same posture as ApplyPoisonPill).
func (a *BrokerApplier) ApplyDuplicate(name string, d queuefault.Duplicate) (func() error, error) {
	group := fmt.Sprintf("tortureu-duplicate-%s-%d", name, time.Now().UnixNano())
	baseURI, err := a.createConsumer(group)
	if err != nil {
		return nil, fmt.Errorf("applier: broker: fault %q: create consumer: %w", name, err)
	}
	if err := a.subscribe(baseURI, d.Topic); err != nil {
		a.deleteConsumer(baseURI)
		return nil, fmt.Errorf("applier: broker: fault %q: subscribe: %w", name, err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		rng := rand.New(rand.NewSource(time.Now().UnixNano()))
		ticker := time.NewTicker(a.poll())
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				a.redeliverOnce(baseURI, d.Topic, d.Rate, rng)
			}
		}
	}()

	var stopOnce sync.Once
	undo := func() error {
		stopOnce.Do(func() {
			close(stop)
			<-done
			a.deleteConsumer(baseURI)
		})
		return nil
	}
	return undo, nil
}

func (a *BrokerApplier) createConsumer(group string) (baseURI string, err error) {
	body := []byte(`{"name":"c1","format":"binary","auto.offset.reset":"earliest"}`)
	respBody, status, err := a.post("/consumers/"+group, "application/vnd.kafka.v2+json", body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", status, respBody)
	}
	var created struct {
		InstanceID string `json:"instance_id"`
	}
	if err := json.Unmarshal(respBody, &created); err != nil {
		return "", err
	}
	return fmt.Sprintf("/consumers/%s/instances/%s", group, created.InstanceID), nil
}

func (a *BrokerApplier) subscribe(baseURI, topic string) error {
	body := []byte(fmt.Sprintf(`{"topics":["%s"]}`, topic))
	respBody, status, err := a.post(baseURI+"/subscription", "application/vnd.kafka.v2+json", body)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return fmt.Errorf("status %d: %s", status, respBody)
	}
	return nil
}

func (a *BrokerApplier) deleteConsumer(baseURI string) {
	req, err := http.NewRequest(http.MethodDelete, a.BaseURL+baseURI, nil)
	if err != nil {
		return
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// redeliverOnce polls one batch of records off the consumer and
// re-publishes a Rate fraction of them, skipping any record already
// tagged by markDuplicate so the loop's own re-publications are never
// re-duplicated (see duplicatePrefix's doc comment).
func (a *BrokerApplier) redeliverOnce(baseURI, topic string, rate float64, rng *rand.Rand) {
	req, err := http.NewRequest(http.MethodGet, a.BaseURL+baseURI+"/records?timeout=1000", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.kafka.binary.v2+json")
	resp, err := a.client().Do(req)
	if err != nil {
		return
	}
	var records []struct {
		Value string `json:"value"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&records)
	resp.Body.Close()

	for _, rec := range records {
		raw, err := base64.StdEncoding.DecodeString(rec.Value)
		if err != nil || isDuplicateMarked(raw) {
			continue
		}
		if !shouldDuplicate(rate, rng.Float64()) {
			continue
		}
		dup := markDuplicate(raw)
		body, _ := json.Marshal(map[string]any{
			"records": []map[string]string{{"value": base64.StdEncoding.EncodeToString(dup)}},
		})
		a.post("/topics/"+topic, "application/vnd.kafka.binary.v2+json", body)
	}
}
