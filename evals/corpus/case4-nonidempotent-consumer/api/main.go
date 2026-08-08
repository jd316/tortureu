// checkout-api is the case-4 E1 fixture's SUT. The planted defect (case 4
// in BENCHMARKS.md's E1 table): a non-idempotent consumer. It applies its
// side effect (incrementing orders_processed_total) for every message it
// reads off the "orders" topic, with no per-message dedup key check at
// all -- so a redelivered copy of a message it already processed gets
// processed again, in full, as if it were new.
//
// It drives Redpanda's Pandaproxy (a Kafka REST Proxy v2-compatible HTTP
// API) directly over plain HTTP, the same dialect internal/applier's
// BrokerApplier speaks -- no Kafka client library, so both sides read and
// write the same wire format.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

const topic = "orders"

func proxyURL() string {
	if v := os.Getenv("REDPANDA_PROXY"); v != "" {
		return v
	}
	return "http://redpanda:8082"
}

var (
	receivedTotal  int64
	processedTotal int64
)

// proxyClient has a bounded timeout on every call to Pandaproxy: an
// unresponsive REST proxy call (e.g. a first-produce auto-create-topic
// hang, observed empirically while building this fixture) should fail one
// attempt fast, not stall the caller for however long Pandaproxy takes.
var proxyClient = &http.Client{Timeout: 3 * time.Second}

// pollClient's timeout must exceed /records?timeout=1000's server-side
// long-poll window.
var pollClient = &http.Client{Timeout: 5 * time.Second}

func post(path, contentType string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodPost, proxyURL()+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := proxyClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// produceOrder publishes one record to the orders topic via Pandaproxy's
// binary v2 dialect -- the same one BrokerApplier.ApplyDuplicate's
// redeliverOnce reads and re-publishes, so a fault-injected duplicate is
// indistinguishable, on the wire, from a genuine order.
func produceOrder(id string) error {
	rec := map[string]any{
		"records": []map[string]string{
			{"value": base64.StdEncoding.EncodeToString([]byte(id))},
		},
	}
	body, _ := json.Marshal(rec)
	respBody, status, err := post("/topics/"+topic, "application/vnd.kafka.binary.v2+json", body)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("produce: status %d: %s", status, respBody)
	}
	return nil
}

// consumerGroup is fixed, not per-run: this is the SUT's own, ordinary
// long-lived consumer, standing in for "the checkout service's order
// processor" -- distinct from BrokerApplier's own throwaway consumer group
// per fault invocation (see its ApplyDuplicate doc comment), which reads
// the same topic independently and re-publishes a fraction of what it
// reads.
const consumerGroup = "checkout-consumers"

type createdConsumer struct {
	InstanceID string `json:"instance_id"`
	BaseURI    string `json:"base_uri"`
}

func createConsumer() (baseURI string, err error) {
	body := []byte(`{"name":"c1","format":"binary","auto.offset.reset":"earliest"}`)
	respBody, status, err := post("/consumers/"+consumerGroup, "application/vnd.kafka.v2+json", body)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("create consumer: status %d: %s", status, respBody)
	}
	var c createdConsumer
	if err := json.Unmarshal(respBody, &c); err != nil {
		return "", err
	}
	return fmt.Sprintf("/consumers/%s/instances/%s", consumerGroup, c.InstanceID), nil
}

func subscribe(baseURI string) error {
	body := []byte(`{"topics":["` + topic + `"]}`)
	_, status, err := post(baseURI+"/subscription", "application/vnd.kafka.v2+json", body)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK {
		return fmt.Errorf("subscribe: status %d", status)
	}
	return nil
}

type consumedRecord struct {
	Value string `json:"value"`
}

// pollOnce reads one batch of records and applies the planted defect: it
// processes every record it reads, unconditionally -- no per-message ID
// ledger, no "have I seen this exact value before" check, nothing that
// would make a redelivered copy a no-op. That is the entire defect; every
// other line in this file is scaffolding around it.
func pollOnce(baseURI string) {
	req, err := http.NewRequest(http.MethodGet, proxyURL()+baseURI+"/records?timeout=1000", nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.kafka.binary.v2+json")
	resp, err := pollClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var records []consumedRecord
	_ = json.NewDecoder(resp.Body).Decode(&records)
	for range records {
		atomic.AddInt64(&processedTotal, 1)
	}
}

func runConsumer() {
	var baseURI string
	for {
		var err error
		baseURI, err = createConsumer()
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	for {
		if err := subscribe(baseURI); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	for {
		pollOnce(baseURI)
	}
}

func main() {
	go runConsumer()

	http.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&receivedTotal, 1)
		id := fmt.Sprintf("order-%d-%d", n, time.Now().UnixNano())
		// Retry produce: Redpanda/Pandaproxy may still be finishing startup
		// for the first few requests of a run.
		var err error
		for i := 0; i < 10; i++ {
			if err = produceOrder(id); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("queued"))
	})

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE orders_received_total counter\norders_received_total %d\n", atomic.LoadInt64(&receivedTotal))
		fmt.Fprintf(w, "# TYPE orders_processed_total counter\norders_processed_total %d\n", atomic.LoadInt64(&processedTotal))
	})

	log.Println("checkout-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
