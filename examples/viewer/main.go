// Command viewer is a read-only browser with opt-in human operator actions. It is a demo
// client of the public HTTP API (spec/openapi.yaml): it serves a small
// static web page and reverse-proxies /v1/* to a Yak server, so the page
// is same-origin with the API.
//
// The proxy passes the Authorization header through unchanged. It never
// extracts, adds, or logs its value. Self-revoke discovery clones its opaque
// request-scoped envelope (D017).
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:embed static
var staticFiles embed.FS

// csp allows only same-origin scripts, styles, and fetches. The page never
// loads remote code and never inserts user content as HTML.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"connect-src 'self'; img-src 'self' data:; base-uri 'none'; " +
	"form-action 'none'; frame-ancestors 'none'; require-trusted-types-for 'script'"

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "address to serve the viewer on")
	api := flag.String("api", "http://127.0.0.1:8090", "base URL of the Yak server")
	operator := flag.Bool("operator", false, "enable the loopback human operator client")
	flag.Parse()
	if *operator && !validOperatorListen(*listen) {
		log.Fatal("viewer: -operator requires -listen 127.0.0.1:<port>")
	}

	target, err := url.Parse(*api)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		log.Fatal("viewer: -api must be an http(s) URL without credentials, query or fragment")
	}

	if *operator && !validOperatorTarget(target) {
		log.Fatal("viewer: -operator requires a loopback HTTP API or HTTPS, without URL credentials, query or fragment")
	}
	handler := viewerHandler(target, *operator)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: event long polls hold a response open for up to
		// limits.max_wait_ms (30 s).
		IdleTimeout: 120 * time.Second,
	}
	log.Printf("viewer: serving http://%s, proxying /v1/ to %s", *listen, target)
	log.Fatal(srv.ListenAndServe())
}

func validOperatorListen(address string) bool {
	host, port, err := net.SplitHostPort(address)
	return err == nil && host == "127.0.0.1" && port != ""
}
func validOperatorTarget(u *url.URL) bool {
	return u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))
}

func routeProblem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	w.Write([]byte(`{"type":"about:blank","status":` + fmt.Sprint(status) + `,"code":"` + code + `","reason":"operator_route_not_allowed"}`))
}

var boardRead = regexp.MustCompile(`^/v1/boards/[^/]+(?:/(?:actors|channels|permissions|events|threads|tasks)|/(?:threads/[^/]+(?:/messages)?|messages/[^/]+|tasks/[^/]+(?:/briefs(?:/[^/]+)?)?|snapshots/[^/]+))?$`)
var snapshotRoute = regexp.MustCompile(`^/v1/boards/[^/]+/snapshots$`)
var snapshotItem = regexp.MustCompile(`^/v1/boards/[^/]+/snapshots/[^/]+$`)
var reactionRoute = regexp.MustCompile(`^/v1/boards/[^/]+/messages/[^/]+/reactions/[^/]+$`)
var operatorRoutes = map[string]*regexp.Regexp{
	"POST":   regexp.MustCompile(`^/v1/boards/[^/]+/(?:threads|threads/[^/]+/messages|tasks|tasks/[^/]+/(?:unblock|claims|release)|tasks/[^/]+/claims/[^/]+/renew)$`),
	"PATCH":  regexp.MustCompile(`^/v1/boards/[^/]+/(?:messages/[^/]+(?:/labels)?|tasks/[^/]+)$`),
	"PUT":    regexp.MustCompile(`^/v1/boards/[^/]+/(?:tasks/[^/]+/assignee|messages/[^/]+/reactions/[^/]+)$`),
	"DELETE": regexp.MustCompile(`^/v1/(?:credentials/[^/]+|boards/[^/]+/messages/[^/]+/reactions/[^/]+)$`),
}

func allowedRoute(method, path string, operator bool) bool {
	if strings.ContainsAny(path, "?#") || strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	strictPath := path
	if (method == "PUT" || method == "DELETE") && reactionRoute.MatchString(path) {
		end := strings.LastIndexByte(path, '/')
		strictPath = path[:end]
		// Decode only the terminal key, once. PathUnescape preserves literal '+'.
		key, err := url.PathUnescape(path[end+1:])
		if err != nil || !utf8.ValidString(key) || key == "" || key == "." || strings.Contains(key, "..") || strings.ContainsAny(key, "/\\%") {
			return false
		}
		for _, r := range key {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	if strings.Contains(strictPath, "%") {
		return false
	}
	if method == "GET" {
		if !operator {
			return true
		}
		return path == "/v1/server" || path == "/v1/me" || path == "/v1/boards" || boardRead.MatchString(path)
	}
	if method == "POST" && snapshotRoute.MatchString(path) || method == "DELETE" && snapshotItem.MatchString(path) {
		return true
	}
	re := operatorRoutes[method]
	return operator && re != nil && re.MatchString(path)
}
func setResponseHeaders(h http.Header) {
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

// ownCredentialStatus verifies this request's exact revoke target. Only the opaque
// authentication envelope reaches discovery; no identity or token association is retained.
func ownCredentialStatus(r *http.Request, target *url.URL) int {
	if target == nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return http.StatusBadGateway
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	discovery := r.Clone(ctx)
	discovery.Method = http.MethodGet
	discovery.URL.Path, discovery.URL.RawPath = "/v1/me", ""
	discovery.URL.RawQuery, discovery.URL.ForceQuery, discovery.URL.Fragment = "", false, ""
	discovery.Body, discovery.GetBody, discovery.ContentLength = nil, nil, 0
	discovery.TransferEncoding, discovery.Trailer = nil, nil
	discovery.RequestURI = ""
	// Clone preserves every authentication header value without extracting a bearer.
	// Discard cookies, proofs, browser identity hints, and all mutation headers.
	for name := range discovery.Header {
		if !strings.EqualFold(name, "Authorization") {
			discovery.Header.Del(name)
		}
	}
	envelope := &httputil.ProxyRequest{Out: discovery}
	envelope.SetURL(target)
	client := &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(discovery)
	if err != nil {
		return http.StatusBadGateway
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return http.StatusUnauthorized
	}
	if response.StatusCode != http.StatusOK {
		return http.StatusBadGateway
	}
	const maxIdentityBytes = 64 * 1024
	body, err := io.ReadAll(io.LimitReader(response.Body, maxIdentityBytes+1))
	if err != nil || len(body) > maxIdentityBytes {
		return http.StatusBadGateway
	}
	var identity struct {
		Credential struct {
			ID string `json:"id"`
		} `json:"credential"`
	}
	if json.Unmarshal(body, &identity) != nil || identity.Credential.ID == "" {
		return http.StatusBadGateway
	}
	if identity.Credential.ID != strings.TrimPrefix(r.URL.Path, "/v1/credentials/") {
		return http.StatusForbidden
	}
	return http.StatusOK
}

func viewerHandler(target *url.URL, operator bool) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			// SetURL rewrites scheme, host, and path prefix. Headers,
			// including Authorization, pass through as sent.
			r.SetURL(target)
			r.Out.Header.Del("Cookie")
		},
		ModifyResponse: func(r *http.Response) error {
			r.Header.Del("Set-Cookie")
			setResponseHeaders(r.Header)
			return nil
		},
		// Flush at once so long-poll responses are not held back.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("viewer: upstream %s %s %d", r.Method, r.URL.Path, http.StatusBadGateway)
			setResponseHeaders(w.Header())
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"type":"about:blank","title":"Bad Gateway","status":502,` +
				`"code":"upstream_unreachable","detail":"the viewer proxy cannot reach the Yak server"}`))
		},
	}

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	files := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		// A reaction key is a path segment, never a query or fragment.
		reactionSuffix := (r.Method == "PUT" || r.Method == "DELETE") && reactionRoute.MatchString(r.URL.EscapedPath()) && (r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" || strings.Contains(r.RequestURI, "#"))
		if reactionSuffix || !allowedRoute(r.Method, r.URL.EscapedPath(), operator) {
			if !operator {
				routeProblem(w, 405, "method_not_allowed")
			} else {
				routeProblem(w, 403, "forbidden")
			}
			return
		}
		// Refuse unsupported routes first so read-only refusals always return 405.
		if r.Method != "GET" && ((r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+r.Host) || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "same-origin")) {
			routeProblem(w, 403, "forbidden")
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/credentials/") {
			if status := ownCredentialStatus(r, target); status != http.StatusOK {
				log.Printf("viewer: upstream %s %s %d", http.MethodGet, "/v1/me", status)
				code := "upstream_identity_unavailable"
				if status == http.StatusForbidden {
					code = "forbidden"
				}
				if status == http.StatusUnauthorized {
					code = "unauthorized"
				}
				routeProblem(w, status, code)
				return
			}
		}
		// ReverseProxy appends upstream headers. Let ModifyResponse (or the
		// ErrorHandler) supply exactly one copy of each required header.
		for _, name := range []string{"X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
			w.Header().Del(name)
		}
		proxy.ServeHTTP(w, r)
	})
	mux.HandleFunc("/viewer-config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if operator {
			w.Write([]byte(`{"operator":true}`))
		} else {
			w.Write([]byte(`{"operator":false}`))
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setResponseHeaders(w.Header())
		mux.ServeHTTP(w, r)
	})
}
