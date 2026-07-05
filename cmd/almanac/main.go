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

	"github.com/mutouyun/almanac/internal/store"
)

// sessionCookie is the name of the cookie holding the session token.
const sessionCookie = "almanac_session"

// sessionTTL is how long a login session stays valid.
const sessionTTL = 7 * 24 * time.Hour

// healthResponse is the payload returned by the /health endpoint.
type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Time    string `json:"time"`
}

// cstZone is China Standard Time (UTC+8), used for human-facing timestamps.
var cstZone = time.FixedZone("CST", 8*60*60)

// dbCheckResponse is the payload returned by the /db-check endpoint.
type dbCheckResponse struct {
	Status string `json:"status"`
	Visits int64  `json:"visits"`
	Time   string `json:"time"`
}

// loginRequest is the payload for POST /api/login.
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginResponse is returned on successful login.
type loginResponse struct {
	Status   string `json:"status"`
	Username string `json:"username"`
}

// errorResponse is a generic error payload.
type errorResponse struct {
	Error string `json:"error"`
}

func loginHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request"})
			return
		}

		u, err := st.VerifyLogin(req.Username, req.Password)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid credentials"})
			return
		}

		token, err := st.CreateSession(u.ID, sessionTTL)
		if err != nil {
			log.Printf("failed to create session: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    token,
			Path:     "/",
			MaxAge:   int(sessionTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})

		_ = json.NewEncoder(w).Encode(loginResponse{
			Status:   "ok",
			Username: u.Username,
		})
	}
}

func logoutHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err == nil {
			_ = st.DeleteSession(c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:   sessionCookie,
			Path:   "/",
			MaxAge: -1,
		})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// whoamiResponse is returned by GET /api/whoami.
type whoamiResponse struct {
	Username string `json:"username"`
}

func whoamiHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u, err := st.UserBySession(c.Value)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(whoamiResponse{Username: u.Username})
	}
}

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

// dbCheckHandler records a visit and returns the running total. It validates
// that SQLite works and that data persists across deployments.
func dbCheckHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		count, err := st.RecordVisit(time.Now().In(cstZone))
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			log.Printf("db-check error: %v", err)
			return
		}
		resp := dbCheckResponse{
			Status: "ok",
			Visits: count,
			Time:   time.Now().In(cstZone).Format(time.RFC3339),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "failed to encode response", http.StatusInternalServerError)
		}
	}
}

func main() {
	// Listen address is configurable via flag or ADDR env var; defaults to :8080.
	addr := flag.String("addr", "", "HTTP listen address, e.g. :8080")
	dbPath := flag.String("db", "", "SQLite database file path")
	flag.Parse()

	listen := *addr
	if listen == "" {
		listen = os.Getenv("ADDR")
	}
	if listen == "" {
		listen = ":8080"
	}

	dbFile := *dbPath
	if dbFile == "" {
		dbFile = os.Getenv("DB_PATH")
	}
	if dbFile == "" {
		dbFile = "data/almanac.db"
	}

	st, err := store.Open(dbFile)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer st.Close()
	log.Printf("database ready at %s", dbFile)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.Handle("/db-check", dbCheckHandler(st))
	mux.Handle("/api/login", loginHandler(st))
	mux.Handle("/api/logout", logoutHandler(st))
	mux.Handle("/api/whoami", whoamiHandler(st))

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
