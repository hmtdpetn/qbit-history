// Command history is the qbit-history server: collectors, SQLite history,
// same-origin API and the built web UI in one process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"qbit-history/internal/api"
	"qbit-history/internal/collector"
	"qbit-history/internal/security"
	"qbit-history/internal/store"
)

func main() {
	listen := flag.String("listen", env("HISTORY_LISTEN", ":28637"), "listen address")
	data := flag.String("data", env("HISTORY_DATA", "./data"), "data directory")
	keyPath := flag.String("master-key", env("HISTORY_MASTER_KEY", "./secrets/master.key"), "credential master key file (32 random bytes, mode 0600)")
	web := flag.String("web", env("HISTORY_WEB", "./webassets/dist"), "static web directory")
	bootstrap := flag.String("bootstrap", os.Getenv("HISTORY_ADMIN_PASSWORD"), "initial administrator password (only used when no local user exists)")
	showVersion := flag.Bool("version", false, "print version and exit")
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit 0/1 (used by the container HEALTHCHECK)")
	flag.Parse()
	if *showVersion {
		fmt.Println("qbit-history", api.Version)
		return
	}
	if *healthcheck {
		os.Exit(probe(*listen))
	}
	logger := log.New(os.Stdout, "qbit-history ", log.LstdFlags|log.LUTC)
	if e := os.MkdirAll(*data, 0700); e != nil {
		logger.Fatal(e)
	}
	if _, e := os.Stat(*keyPath); os.IsNotExist(e) {
		if e = os.MkdirAll(filepath.Dir(*keyPath), 0700); e != nil {
			logger.Fatal(e)
		}
		if e = security.CreateKey(*keyPath); e != nil {
			logger.Fatalf("create master key: %v", e)
		}
		logger.Printf("generated master key at %s; back it up separately from the database", *keyPath)
	}
	key, e := security.LoadKey(*keyPath)
	var perm *security.PermissionError
	if errors.As(e, &perm) && os.Getenv("HISTORY_ALLOW_INSECURE_KEY_PERMS") == "1" {
		// Windows/SMB bind mounts always report 0777 and cannot be chmod-ed.
		logger.Printf("WARNING: %v — continuing because HISTORY_ALLOW_INSECURE_KEY_PERMS=1. Never set this on the server.", e)
		key, e = security.LoadKeyAllowingWorldReadable(*keyPath)
	}
	if e != nil {
		if errors.As(e, &perm) {
			logger.Fatalf("%v\n  fix: chmod 600 %s && chown 65532:65532 %s\n  (local testing on a filesystem without Unix modes: set HISTORY_ALLOW_INSECURE_KEY_PERMS=1)", e, *keyPath, *keyPath)
		}
		logger.Fatal(e)
	}
	db, e := store.Open(filepath.Join(*data, "history.sqlite"))
	if e != nil {
		logger.Fatal(e)
	}
	defer db.Close()
	if db.UserName() == "" {
		if len(*bootstrap) < 12 {
			logger.Fatal("first start requires HISTORY_ADMIN_PASSWORD (12-72 characters); no default password exists")
		}
		if e = db.EnsureAdmin("admin", *bootstrap); e != nil {
			logger.Fatal(e)
		}
		logger.Print("administrator account created from HISTORY_ADMIN_PASSWORD; remove the variable afterwards")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mgr := collector.NewManager(ctx, db, key)
	if e = mgr.Reload(); e != nil {
		logger.Printf("collector initialization warning: %v", e)
	}
	go mgr.RunWriter(ctx)
	srv := &http.Server{Addr: *listen, Handler: api.New(db, mgr, key, *web, logger).Handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 12 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		mgr.StopCollectors()
		done := make(chan struct{})
		go func() { mgr.Flush(); db.Checkpoint(true); close(done) }()
		select {
		case <-done:
		case <-shut.Done():
			logger.Print("final flush did not finish in time; uncommitted tail is lost")
		}
		srv.Shutdown(shut)
	}()
	logger.Printf("qbit-history %s listening on %s (SQLite %s)", api.Version, *listen, db.SQLite)
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		logger.Fatal(e)
	}
	logger.Print("stopped")
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// probe is the container health check: only this service's own liveness counts;
// storage pauses and offline qB instances are reported in the body, not as failure.
func probe(listen string) int {
	host := listen
	if len(host) > 0 && host[0] == ':' {
		host = "127.0.0.1" + host
	}
	c := &http.Client{Timeout: 3 * time.Second}
	res, e := c.Get("http://" + host + "/healthz")
	if e != nil {
		fmt.Println("unhealthy:", e)
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		fmt.Println("unhealthy: status", res.StatusCode)
		return 1
	}
	return 0
}
