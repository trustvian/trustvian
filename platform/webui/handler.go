// Package webui serves the browser control plane's static assets.
//
// This package is deliberately ignorant. It knows about bytes, content types
// and security headers; it knows nothing about projects, evaluations, gates or
// SQLite. Everything the browser learns, it learns by calling /v1 itself —
// the same contract the CLI and TUI use.
//
// NewHandler takes no arguments, and that signature is the architectural
// boundary rather than a convenience. ADR 0023 observed that each interface is
// a plausible place to "just compute the gate result here", and for a package
// living inside the platform module every service is one import away. A
// package that is never handed a ControlPlane cannot grow into one. See
// docs/adr/0036-webui-is-a-same-origin-adapter-over-v1.md.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
)

// assetFS holds the shipped application.
//
// Embedded rather than read from disk: there is no web root to point at the
// wrong directory, no deployment step that can forget the files, and no path
// derived from a request that could escape anywhere. A release artifact stays
// self-contained.
//
//go:embed assets
var assetFS embed.FS

// contentSecurityPolicy is the page's capability list.
//
// default-src 'none' first, so anything not named below is denied rather than
// inherited. Every fetch directive that is allowed is 'self' — the UI is
// same-origin with the API it calls, which is what makes connect-src 'self'
// sufficient and CORS unnecessary.
//
// There is no 'unsafe-inline' and no 'unsafe-eval', which is only achievable
// because the shipped HTML carries no inline <script> and no inline <style>.
// That constraint is the reason the policy means what it appears to mean; a
// policy with 'unsafe-inline' in it would be decoration.
//
// This is browser hardening, not authentication. It bounds what the page may
// do and says nothing about who may open it — task 070 owns that.
const contentSecurityPolicy = "default-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"connect-src 'self'; " +
	"img-src 'self'; " +
	"font-src 'none'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'none'; " +
	"form-action 'self'"

// Content types are stated explicitly rather than inferred from the file
// extension, because nosniff makes a wrong or missing type a broken page
// instead of a guessed one. Charset is always named: a JS file served without
// one is decoded by the browser's locale guess.
const (
	typeHTML = "text/html; charset=utf-8"
	typeJS   = "text/javascript; charset=utf-8"
	typeCSS  = "text/css; charset=utf-8"
)

// shellPath is the one route that serves the application shell.
const shellPath = "/"

// assetPrefix is the only other path space this handler answers.
const assetPrefix = "/assets/"

// asset is one file, resolved once at construction.
type asset struct {
	body        []byte
	contentType string
}

// handler serves a fixed map of paths.
//
// A map lookup rather than http.FileServer, for three reasons that all matter
// more than the convenience: FileServer serves directory listings, it
// redirects paths in ways that turn "/assets" into a 301 nobody asked for, and
// it resolves a request-derived path against a filesystem. Here the set of
// reachable paths is finite, known at construction, and cannot be extended by
// anything a client sends.
type handler struct {
	assets map[string]asset
}

// NewHandler returns the static WebUI handler.
//
// No parameters, and none may be added: see the package comment. The error
// exists for embed verification at startup, so a missing asset fails the
// runtime rather than 404ing at a developer later.
func NewHandler() (http.Handler, error) {
	sub, err := fs.Sub(assetFS, "assets")
	if err != nil {
		return nil, err
	}

	h := &handler{assets: make(map[string]asset)}

	// Walk what is embedded rather than listing filenames here. A new asset
	// is then reachable by adding the file, and a renamed one cannot leave a
	// stale entry behind pointing at nothing.
	err = fs.WalkDir(sub, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, readErr := fs.ReadFile(sub, name)
		if readErr != nil {
			return readErr
		}
		contentType, known := contentTypeFor(name)
		if !known {
			// Refuse at construction rather than serve bytes with a guessed
			// type. An asset kind nobody chose a content type for is a
			// mistake worth failing on, not a default worth inventing.
			return &unknownAssetError{name: name}
		}
		if name == "index.html" {
			h.assets[shellPath] = asset{body: body, contentType: contentType}
			return nil
		}
		h.assets[assetPrefix+name] = asset{body: body, contentType: contentType}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := h.assets[shellPath]; !ok {
		return nil, &unknownAssetError{name: "index.html (missing)"}
	}
	return h, nil
}

// unknownAssetError names an embedded file with no declared content type.
type unknownAssetError struct{ name string }

func (e *unknownAssetError) Error() string {
	return "webui: no content type declared for embedded asset " + e.name
}

// contentTypeFor maps an embedded filename to its declared type.
func contentTypeFor(name string) (string, bool) {
	switch path.Ext(name) {
	case ".html":
		return typeHTML, true
	case ".js":
		return typeJS, true
	case ".css":
		return typeCSS, true
	default:
		return "", false
	}
}

// AssetPaths lists every path this handler serves, sorted.
//
// Exported for the local runtime's tests and for documentation, so a routing
// test enumerates what actually exists instead of repeating a filename list
// that can drift from the embedded set.
func AssetPaths() []string {
	h, err := NewHandler()
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(h.(*handler).assets))
	for p := range h.(*handler).assets {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// ContentSecurityPolicy returns the policy this handler sends.
//
// Exported so a test can assert what it forbids without re-deriving the
// string, and so documentation quotes the shipped value rather than a copy.
func ContentSecurityPolicy() string { return contentSecurityPolicy }

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Headers before any status decision, so a 404 and a 405 are protected
	// exactly as the shell is. An error page is still a page, and a policy
	// that only covers the success path is a policy with a hole in it.
	h.setSecurityHeaders(w)

	// Read-only surface: the browser mutates through /v1, never through the
	// asset space. Anything else is refused rather than silently treated as a
	// GET, and Allow says so.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	entry, ok := h.assets[r.URL.Path]
	if !ok {
		// A plain 404, and deliberately not the shell. Serving index.html for
		// unknown paths is the single-page-app habit that makes every typo
		// look like a working page, and it would mean a mistyped API path
		// answered 200 with markup.
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", entry.contentType)
	// Length is set explicitly because HEAD must report it without a body.
	w.Header().Set("Content-Length", itoa(len(entry.body)))

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(entry.body)
}

// setSecurityHeaders applies the same set to every response.
func (h *handler) setSecurityHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	// With explicit content types above, sniffing can only ever disagree with
	// the truth — and a sniffed type is how a text file becomes a script.
	header.Set("X-Content-Type-Options", "nosniff")
	// The URL fragment carries the watched run ID. no-referrer keeps local
	// identifiers out of any outbound request a future asset might make.
	header.Set("Referrer-Policy", "no-referrer")
	// no-store on every asset, not just the shell. The port changes on each
	// restart, so a cached bundle from a previous runtime could otherwise run
	// against a different database — and stale JavaScript is as wrong as
	// stale HTML.
	header.Set("Cache-Control", "no-store")
	// The UI is never a frame, and frame-ancestors 'none' already says so to
	// anything modern. This is the older spelling, kept for browsers that
	// honour it and nothing else.
	header.Set("X-Frame-Options", "DENY")
}

// itoa avoids importing strconv for one call on a small non-negative length.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}

// interface check: the handler never grows a dependency it can be handed.
var _ http.Handler = (*handler)(nil)

// assetNames is used by tests to confirm the embedded set is what ships.
func assetNames() []string {
	var names []string
	_ = fs.WalkDir(assetFS, "assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		names = append(names, strings.TrimPrefix(name, "assets/"))
		return nil
	})
	sort.Strings(names)
	return names
}
