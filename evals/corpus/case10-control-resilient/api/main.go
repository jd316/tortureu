// checkout-api is the case-10 E1 fixture's SUT, and it is the corpus's
// counter-example: a service that is *correct*.
//
// Case 8 proves TortureU invents nothing when there is no fault. This case
// proves the harder thing — that it invents nothing when there IS a real
// fault and the service handles it. A dependency is deliberately slowed by
// 3s; this service has a 500ms deadline and a degraded-but-valid fallback,
// so it answers 200 quickly and its assertions hold.
//
// A tool that reports a finding here is reporting the fault it injected
// rather than a defect in the service, which is the subtlest way an
// attribution tool can be wrong.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// resilientClient is the whole difference from case 1: a request deadline.
// Under a 3s dependency stall this returns an error in 500ms rather than
// waiting however long the dependency takes.
var resilientClient = &http.Client{Timeout: 500 * time.Millisecond}

func depURL() string {
	if v := os.Getenv("DEP_URL"); v != "" {
		return v
	}
	return "http://dep:9090/slow"
}

func main() {
	http.HandleFunc("/checkout", func(w http.ResponseWriter, r *http.Request) {
		resp, err := resilientClient.Get(depURL())
		if err != nil {
			// Degrade, do not fail: the dependency is optional enrichment,
			// so a timeout costs the caller a field, not their request.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok (degraded)"))
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log.Println("checkout-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
