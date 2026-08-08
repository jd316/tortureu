// checkout-api is the case-2 E1 fixture's SUT. The planted defect (case 2 in
// BENCHMARKS.md's E1 table): retrying a downstream call with no cap and no
// backoff. When the dependency goes down, this amplifies one inbound
// request into hundreds of immediate outbound retries -- a retry storm --
// instead of failing fast.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// depClient has a short per-attempt timeout so a downed dependency fails
// fast per attempt -- the storm comes from what happens next, not from
// this timeout being long.
var depClient = &http.Client{Timeout: 100 * time.Millisecond}

// maxRetries is deliberately large and retryDelay is deliberately zero: the
// planted defect is "no cap or backoff" (BENCHMARKS.md case 2), not
// "infinite retries" -- 30 immediate retries at a 100ms per-attempt timeout
// is already a 30x request amplification and a 3s worst-case request
// latency, enough to be clearly measurable within an eval-length run, while
// still letting the process terminate a request eventually instead of
// hanging forever.
const maxRetries = 30

func depURL() string {
	if v := os.Getenv("DEP_URL"); v != "" {
		return v
	}
	return "http://dep:9090/slow"
}

func main() {
	http.HandleFunc("/checkout", func(w http.ResponseWriter, r *http.Request) {
		var lastErr error
		for i := 0; i < maxRetries; i++ {
			resp, err := depClient.Get(depURL())
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
				return
			}
			lastErr = err
			// No backoff: the next attempt fires immediately (the planted
			// defect), not after a delay.
		}
		http.Error(w, lastErr.Error(), http.StatusBadGateway)
	})
	log.Println("checkout-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
