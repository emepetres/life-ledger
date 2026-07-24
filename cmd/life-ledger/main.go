// Command life-ledger boots the Life Ledger web server: a single static binary
// that serves the home page, the vendored htmx asset, and a health check.
package main

import (
	"crypto/rand"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	// Embed the timezone database in the binary (ADR-0005). The server resolves
	// "today" from the local wall clock, but the distroless runtime image ships no
	// zoneinfo; without this a container would fall back to UTC and file a
	// late-evening expense under the wrong calendar day. With tzdata embedded, the
	// TZ env var (set to Europe/Madrid in production) selects the zone. Do not
	// remove this blank import — nothing references it directly.
	_ "time/tzdata"

	"github.com/emepetres/life-ledger/internal/auth"
	"github.com/emepetres/life-ledger/internal/blobbackup"
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

	// Durability seam (ADR-0003): selecting the backup sink is configuration, not
	// code. With LIFELEDGER_BACKUP_BLOB_URL set (production on ephemeral storage),
	// wire the Azure Blob sink so the store backs up after every write and restores
	// on a cold boot. Unset (local QA, CI), no sink is wired — the store is a pure
	// local file and no Azure credential or network call is ever involved.
	var opts []store.Option
	if blobURL := os.Getenv("LIFELEDGER_BACKUP_BLOB_URL"); blobURL != "" {
		sink, err := blobbackup.New(blobURL)
		if err != nil {
			log.Fatalf("configuring blob backup: %v", err)
		}
		opts = append(opts, store.WithBackup(sink))
	}

	st, err := store.Open(dbPath, opts...)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Real authentication is always on (ADR-0004): identical path locally and in
	// production, so the shipped auth is exercised in local QA too.
	guard, err := auth.NewGuard(authConfig())
	if err != nil {
		log.Fatalf("configuring auth: %v", err)
	}

	handler, err := server.New(st, server.WithGuard(guard))
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

// authConfig assembles the auth settings from the environment (ADR-0004):
//
//   - LIFELEDGER_PASSWORD_HASH — the bcrypt hash of the shared password, required.
//     Generate one with `make hash-password`; the plaintext is never stored. A
//     missing hash is fatal, so the app never boots unprotected.
//   - LIFELEDGER_SECURE_COOKIE — sets the cookie's Secure flag. Off by default so
//     local http://localhost QA works out of the box; production sets it to true.
//   - LIFELEDGER_SESSION_KEY — the cookie signing secret; rotating it logs everyone
//     out. Required in production (Secure on): a missing key there is fatal rather
//     than silently rotating on every restart. For local QA (Secure off) it may be
//     omitted, in which case a random ephemeral key is generated per boot.
func authConfig() auth.Config {
	hash := os.Getenv("LIFELEDGER_PASSWORD_HASH")
	if hash == "" {
		log.Fatalf("LIFELEDGER_PASSWORD_HASH is required — generate one with `make hash-password` and set it in the environment")
	}

	secure := secureCookieEnv()

	var secret []byte
	if key := os.Getenv("LIFELEDGER_SESSION_KEY"); key != "" {
		secret = []byte(key)
	} else if secure {
		// In production a missing signing key would silently rotate on every
		// restart, logging everyone out; fail fast instead of shipping that.
		log.Fatalf("LIFELEDGER_SESSION_KEY is required when LIFELEDGER_SECURE_COOKIE is on (production); set a stable secret")
	} else {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Fatalf("generating ephemeral session key: %v", err)
		}
		log.Printf("warning: LIFELEDGER_SESSION_KEY unset; using an ephemeral key (sessions won't survive a restart)")
	}

	return auth.Config{
		PasswordHash:  []byte(hash),
		SessionSecret: secret,
		Secure:        secure,
	}
}

// secureCookieEnv reads LIFELEDGER_SECURE_COOKIE as a boolean, defaulting to
// false (local http://localhost). A set-but-unparseable value is a likely
// misconfiguration that would silently weaken the cookie, so it is warned about
// rather than accepted quietly.
func secureCookieEnv() bool {
	v := os.Getenv("LIFELEDGER_SECURE_COOKIE")
	if v == "" {
		return false
	}
	secure, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("warning: LIFELEDGER_SECURE_COOKIE=%q is not a valid boolean; treating as false", v)
		return false
	}
	return secure
}
