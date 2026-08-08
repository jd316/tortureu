// checkout-api is the case-5 E1 fixture's SUT. The planted defect (case 5
// in BENCHMARKS.md's E1 table): no circuit breaker on the cascade path to
// "dep". This service has a deliberately small, shared worker pool (a
// realistic stand-in for a bounded thread/goroutine pool, or a small
// connection pool serving all routes) -- with a circuit breaker, calls to
// a failing dep would fail fast and free their worker slot quickly; without
// one, a slow dep call occupies a slot for its full duration and, once
// enough slow calls are in flight, healthy unrelated traffic (/health, on
// the same shared pool) queues behind them too. One slow dependency takes
// the whole service down, not just the /checkout path.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
)

// noTimeoutClient has no timeout, same defect shape as case 1: nothing
// bounds how long a call to dep can occupy a worker slot.
var noTimeoutClient = &http.Client{}

// workerSlots is the case's cascade mechanism: every request, regardless of
// route, must acquire a slot before doing any work and releases it when
// done. It is deliberately small and deliberately shared across routes --
// the thing a circuit breaker on the dep call would normally protect.
var workerSlots = make(chan struct{}, 5)

func depURL() string {
	if v := os.Getenv("DEP_URL"); v != "" {
		return v
	}
	return "http://dep:9090/slow"
}

func withSlot(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		workerSlots <- struct{}{}
		defer func() { <-workerSlots }()
		h(w, r)
	}
}

func main() {
	http.HandleFunc("/checkout", withSlot(func(w http.ResponseWriter, r *http.Request) {
		resp, err := noTimeoutClient.Get(depURL())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	// /health calls no dependency at all -- with a circuit breaker isolating
	// the /checkout path, this would stay fast no matter what dep does.
	http.HandleFunc("/health", withSlot(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	log.Println("checkout-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
