// api is the case-8 E1 control fixture: no planted defect. It must never
// produce a finding — case 8 is the corpus's control, and BENCHMARKS.md's
// launch gate requires zero false positives on it.
package main

import (
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log.Println("control-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
