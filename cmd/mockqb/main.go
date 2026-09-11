// Command mockqb runs the simulated qBittorrent WebUI API for local demos.
// It advances traffic scenarios once per second and exposes /_mock/requests
// so you can verify the history service only used the read-only whitelist.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"qbit-history/internal/mockqb"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:18080", "listen address")
	n := flag.Int("torrents", 24, "torrent count")
	user := flag.String("user", "admin", "username")
	pass := flag.String("pass", "adminadmin", "password")
	flag.Parse()
	m := mockqb.New()
	m.Username, m.Password = *user, *pass
	m.Populate(*n)
	go func() {
		for range time.Tick(time.Second) {
			m.Step(1)
		}
	}()
	mux := http.NewServeMux()
	mux.Handle("/", m.Handler())
	mux.HandleFunc("/_mock/requests", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"requests": m.Requests(), "violations": m.Violations()})
	})
	mux.HandleFunc("/_mock/mode", func(w http.ResponseWriter, r *http.Request) {
		m.SetMode(r.URL.Query().Get("mode"))
		fmt.Fprintln(w, "mode set")
	})
	mux.HandleFunc("/_mock/remove", func(w http.ResponseWriter, r *http.Request) {
		m.Remove(r.URL.Query()["key"]...)
		fmt.Fprintln(w, "removed")
	})
	log.Printf("mock qBittorrent on http://%s (user %s) with %d torrents", *addr, *user, *n)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
