// Command almanac is the HTTP backend server for the Almanac personal platform.
//
// MVP stage: a minimal HTTP service exposing a /health endpoint, used to
// validate the end-to-end CI/CD pipeline (build -> test -> deploy).
package main

import (
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

// healthResponse is the payload returned by the /health endpoint.
type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Time    string `json:"time"`
}

// cstZone is China Standard Time (UTC+8), used for human-facing timestamps.
var cstZone = time.FixedZone("CST", 8*60*60)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp := healthResponse{
		Status:  "ok",
		Service: "almanac",
		Time:    time.Now().In(cstZone).Format(time.RFC3339),
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

func main() {
	// Listen address is configurable via flag or ADDR env var; defaults to :8080.
	addr := flag.String("addr", "", "HTTP listen address, e.g. :8080")
	flag.Parse()

	listen := *addr
	if listen == "" {
		listen = os.Getenv("ADDR")
	}
	if listen == "" {
		listen = ":8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)

	// Serve the embedded frontend (Astro build output) at the root path.
	// staticFS is provided by the build-tagged files (embed_dist.go /
	// embed_stub.go) so the server works with or without a built frontend.
	if sub, err := fs.Sub(staticFS, staticRoot); err == nil {
		mux.Handle("/", http.FileServer(http.FS(sub)))
	}

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("almanac starting, listening on %s", listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server failed: %v", err)
	}
}
