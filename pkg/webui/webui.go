// Package webui embeds the easysh control panel: a Svelte SPA from the
// easysh-webui repo (github.com/easydev-org/easysh-webui), built into dist/ and
// embedded here, served at "/" so it is same-origin with the HTTP API — the
// browser needs only a bearer token, no CORS. dist/ is NOT committed (easysh is
// not a container service): ./build.sh builds the easysh-webui repo into dist/
// and then compiles the binaries, so the SPA is embedded into the exported
// binary.
//
// The panel offers: an xterm.js terminal that runs easysh over a WebSocket, a
// Jobs list, and — the point of easysh — a dataflow-graph view for each job
// showing every pipe, redirect and fd, with each intermediate result readable.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the SPA: static assets by exact path, index.html for the rest.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// SPA fallback
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
