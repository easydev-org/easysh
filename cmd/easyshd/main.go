// Command easyshd is the easysh daemon: long-lived sessions, an async job
// scheduler that detaches long-running jobs, and SQLite persistence. It serves
// the wire protocol on a unix socket (local CLI) and optionally TCP (worker).
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/easydev-org/easysh/pkg/daemon"
	"github.com/easydev-org/easysh/pkg/httpapi"
	"github.com/easydev-org/easysh/pkg/store"
	"github.com/easydev-org/easysh/pkg/webui"
)

func main() {
	fs := flag.NewFlagSet("easyshd", flag.ContinueOnError)
	sock := fs.String("socket", defaultSock(), "unix socket to listen on")
	tcp := fs.String("tcp", "", "also listen on this TCP address (for the worker)")
	httpAddr := fs.String("http", "", "serve the HTTP API + WebUI on this address (e.g. :8080)")
	httpToken := fs.String("http-token", os.Getenv("EASYSH_HTTP_TOKEN"), "bearer token for the HTTP API (empty = no auth)")
	db := fs.String("db", defaultDB(), "SQLite database path")
	dir := fs.String("dir", "", "default working directory (default: cwd)")
	detach := fs.Duration("detach", 10*time.Second, "detach a foreground job after this long")
	persist := fs.Bool("persist", true, "persist jobs and flow graphs to SQLite")
	_ = fs.Parse(os.Args[1:])

	if *dir == "" {
		*dir, _ = os.Getwd()
	}

	var st *store.Store
	if *persist {
		os.MkdirAll(filepath.Dir(*db), 0o755)
		var err error
		st, err = store.Open(*db)
		if err != nil {
			fmt.Fprintln(os.Stderr, "easyshd: open store:", err)
			os.Exit(1)
		}
		defer st.Close()
	}

	env := os.Environ()
	srv := daemon.NewServer(*dir, env, st, *detach)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		fmt.Fprintln(os.Stderr, "\neasyshd: shutting down")
		cancel()
		time.Sleep(100 * time.Millisecond)
		os.Exit(0)
	}()

	if *tcp != "" {
		go func() {
			if err := srv.ServeTCP(ctx, *tcp); err != nil {
				fmt.Fprintln(os.Stderr, "easyshd: tcp:", err)
			}
		}()
		fmt.Fprintf(os.Stderr, "easyshd: listening on tcp %s\n", *tcp)
	}
	if *httpAddr != "" {
		api := (&httpapi.Server{Srv: srv, Store: st, Token: *httpToken, UI: webui.Handler()}).WithContext(ctx)
		go func() {
			fmt.Fprintf(os.Stderr, "easyshd: HTTP API + WebUI on http://%s\n", *httpAddr)
			if err := http.ListenAndServe(*httpAddr, api.Handler()); err != nil {
				fmt.Fprintln(os.Stderr, "easyshd: http:", err)
			}
		}()
	}
	fmt.Fprintf(os.Stderr, "easyshd: listening on unix %s (detach after %s)\n", *sock, *detach)
	if err := srv.ServeUnix(ctx, *sock); err != nil {
		fmt.Fprintln(os.Stderr, "easyshd:", err)
		os.Exit(1)
	}
}

func defaultSock() string {
	if p := os.Getenv("EASYSH_SOCK"); p != "" {
		return p
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return x + "/easysh.sock"
	}
	return "/tmp/easysh.sock"
}

func defaultDB() string {
	if p := os.Getenv("EASYSH_DB"); p != "" {
		return p
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".easysh", "jobs.db")
	}
	return "/tmp/easysh-jobs.db"
}
