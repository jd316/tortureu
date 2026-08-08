// dep is the case-5 fixture's downstream dependency: a plain HTTP service
// that responds fast under normal conditions. E1 injects latency at the
// network layer (Toxiproxy), not here.
package main

import (
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log.Println("dep listening on :9090")
	log.Fatal(http.ListenAndServe(":9090", nil))
}
