// Command life-ledger boots the Life Ledger web server: a single static binary
// that serves the home page, the vendored htmx asset, and a health check.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/emepetres/life-ledger/internal/server"
	"github.com/emepetres/life-ledger/internal/store"
)

// defaultAddr is the listen address when LIFELEDGER_ADDR is unset. It binds all
// interfaces so the container platform can reach it; local QA uses the same.
const defaultAddr = ":8080"

func main() {
	addr := os.Getenv("LIFELEDGER_ADDR")
	if addr == "" {
		addr = defaultAddr
	}

	// Unified startup path (ADR-0003): open the SQLite DB at the configured path,
	// creating the file and parent dir if absent and applying embedded migrations,
	// before serving. Identical locally and in production — only the path differs.
	dbPath := os.Getenv("LIFELEDGER_DB_PATH")
	if dbPath == "" {
		dbPath = store.DefaultDBPath
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer func() { _ = st.Close() }()
	log.Printf("database ready at %s", dbPath)

	handler, err := server.New()
	if err != nil {
		log.Fatalf("building server: %v", err)
	}

	// Explicit timeouts rather than the zero-value defaults, so a slow or stalled
	// client can't tie up a connection indefinitely (Slowloris).
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("Life Ledger listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
