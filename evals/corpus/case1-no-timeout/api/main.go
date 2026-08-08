// checkout-api is the case-1 E1 fixture's SUT. The planted defect (case 1
// in evals/CORPUS.md / BENCHMARKS.md's E1 table): it calls its downstream
// dependency with the zero-value http.Client{} — no Timeout field set — so
// a slow dependency stalls every in-flight request instead of failing fast.
// That single line is the fixture's only defect; everything else here is
// scaffolding.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
)

// noTimeoutClient is the planted defect: the zero-value http.Client has no
// deadline of any kind (no Timeout, no context deadline is applied by the
// caller either), so a dependency that goes slow makes this client wait
// however long the dependency takes.
var noTimeoutClient = &http.Client{}

func depURL() string {
	if v := os.Getenv("DEP_URL"); v != "" {
		return v
	}
	return "http://dep:9090/slow"
}

func main() {
	http.HandleFunc("/checkout", func(w http.ResponseWriter, r *http.Request) {
		resp, err := noTimeoutClient.Get(depURL())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
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
