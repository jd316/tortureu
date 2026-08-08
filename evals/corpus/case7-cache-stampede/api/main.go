// catalog-api is the case-7 E1 fixture's SUT. The planted defect (case 7 in
// BENCHMARKS.md's E1 table): a cache-aside read with a short TTL and no
// stampede protection (no singleflight, no lock, no early refresh). At
// every TTL boundary, every concurrent request that misses the cache at
// the same moment independently pays the full "expensive backend" cost
// instead of one request refilling the cache for the rest -- a thundering
// herd, periodic with the TTL. No faults: entry is needed: the defect is a
// timing property of sustained load against a short TTL.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	cacheKey = "product:1"
	ttl      = 2 * time.Second
)

func redisAddr() string {
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		return v
	}
	return "redis:6379"
}

// expensiveBackend stands in for whatever a real cache miss would recompute
// -- a DB join, a render, an upstream call. Its cost is what a stampede
// multiplies.
func expensiveBackend() string {
	time.Sleep(100 * time.Millisecond)
	return "product-1-payload"
}

func main() {
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr()})
	ctx := context.Background()

	http.HandleFunc("/product", func(w http.ResponseWriter, r *http.Request) {
		val, err := rdb.Get(r.Context(), cacheKey).Result()
		if err == nil {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(val))
			return
		}
		// Cache miss: every concurrent goroutine that lands here at the
		// same TTL boundary independently recomputes and re-writes --
		// no singleflight, no lock, no "one winner" protection.
		val = expensiveBackend()
		_ = rdb.Set(ctx, cacheKey, val, ttl).Err()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(val))
	})
	log.Println("catalog-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
