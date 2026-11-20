// Command viewer is a view-only browser for Abby boards. It is a demo
// client of the public HTTP API (spec/openapi.yaml): it serves a small
// static web page and reverse-proxies /v1/* to an Abby server, so the page
// is same-origin with the API.
//
// The proxy passes the Authorization header through unchanged. It never
// reads, adds, or logs it.
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

//go:embed static
var staticFiles embed.FS

// csp allows only same-origin scripts, styles, and fetches. The page never
// loads remote code and never inserts user content as HTML.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"connect-src 'self'; img-src 'self' data:; base-uri 'none'; " +
	"form-action 'none'; frame-ancestors 'none'"

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "address to serve the viewer on")
	api := flag.String("api", "http://127.0.0.1:8090", "base URL of the Abby server")
	flag.Parse()

	target, err := url.Parse(*api)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		log.Fatalf("viewer: -api must be an http(s) URL, got %q", *api)
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			// SetURL rewrites scheme, host, and path prefix. Headers,
			// including Authorization, pass through as sent.
			r.SetURL(target)
		},
		// Flush at once so long-poll responses are not held back.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("viewer: upstream %s %s: %v", r.Method, r.URL.Path, err)
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Bad Gateway","status":502,` +
				`"code":"upstream_unreachable","detail":"the viewer proxy cannot reach the Abby server"}`))
		},
	}

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	files := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.Handle("/v1/", proxy)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: event long polls hold a response open for up to
		// limits.max_wait_ms (30 s).
		IdleTimeout: 120 * time.Second,
	}
	log.Printf("viewer: serving http://%s, proxying /v1/ to %s", *listen, target)
	log.Fatal(srv.ListenAndServe())
}
