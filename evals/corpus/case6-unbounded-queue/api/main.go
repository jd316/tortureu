// ingest-api is the case-6 E1 fixture's SUT. The planted defect (case 6 in
// BENCHMARKS.md's E1 table): an unbounded in-memory queue. Every request
// appends a chunk of data to a slice that a slow background worker drains
// far more slowly than requests can arrive; under sustained load the queue
// -- and the process's memory -- grows without bound. No faults: entry is
// needed for this case: the defect is a static capacity problem that only
// shows up under sustained load, matching BENCHMARKS.md's "OOM under
// sustained spike".
package main

import (
	"log"
	"net/http"
	"sync"
	"time"
)

// chunkSize is deliberately large (4MB per accepted request) so that
// sustained load accumulates memory fast enough to matter within an
// eval-length run, against docker-compose.yml's deliberately small
// mem_limit. A first attempt at 1MB was flaky: the Go GC, under cgroup
// memory pressure, reclaimed already-drained slices aggressively enough to
// oscillate just under the limit rather than reliably exceeding it (an E1
// finding in its own right -- documented in evals/results/summary.json).
// Larger chunks mean fewer, bigger live allocations in flight at any one
// time, which the GC has less room to interleave-collect around.
const chunkSize = 4 << 20

var (
	mu    sync.Mutex
	queue [][]byte
)

// drain is deliberately slow (far slower than the load this fixture drives
// generates) -- the mechanism that makes the queue actually grow, not just
// exist.
func drain() {
	for {
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		if len(queue) > 0 {
			queue = queue[1:]
		}
		mu.Unlock()
	}
}

func main() {
	go drain()
	http.HandleFunc("/enqueue", func(w http.ResponseWriter, r *http.Request) {
		item := make([]byte, chunkSize)
		// Actually write every page: a freshly mmap'd, never-written Go
		// slice can sit on the kernel's shared zero page and never become
		// resident, so cgroup memory accounting (and the OOM killer) never
		// sees it -- discovered empirically in this eval (a first attempt
		// at this fixture queued hundreds of "logical" megabytes without
		// RSS crossing 20MB). Filling it with real bytes forces every page
		// to fault in for real.
		for i := range item {
			item[i] = byte(i)
		}
		mu.Lock()
		queue = append(queue, item) // unbounded: no length cap anywhere
		depth := len(queue)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("queued"))
		_ = depth
	})
	log.Println("ingest-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
