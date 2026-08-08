// orders-api is the case-3 E1 fixture's SUT. The planted defect (case 3 in
// BENCHMARKS.md's E1 table): a connection pool capped at 5 behind a load
// that needs far more concurrency than that -- pool exhaustion, not a
// network fault. There is no faults: entry in this case's torture.yaml;
// the defect is a static config value that only shows up under load.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func dsn() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://postgres:postgres@postgres:5432/postgres?sslmode=disable"
}

func main() {
	cfg, err := pgxpool.ParseConfig(dsn())
	if err != nil {
		log.Fatalf("parse pool config: %v", err)
	}
	// The planted defect: a pool of 5, hardcoded, with no relationship to
	// the load this service is expected to carry.
	cfg.MaxConns = 5

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	http.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		// A deliberately non-trivial hold time per request -- representative
		// of a real query, not instant -- so that at even modest request
		// rates, more concurrent requests are in flight than the pool of 5
		// can serve, and the rest queue for a connection.
		_, err := pool.Exec(ctx, "SELECT pg_sleep(0.2)")
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log.Println("orders-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
