package webui

// Handler tests: what is served, with which headers, and what is refused.
//
// Internal to the package so the embedded set and the content-type table can be
// checked directly rather than inferred from responses.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := NewHandler()
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder
}

// TestShellIsServedAtRoot is the entry point a developer opens.
func TestShellIsServedAtRoot(t *testing.T) {
	response := get(t, newHandler(t), http.MethodGet, "/")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != typeHTML {
		t.Errorf("Content-Type = %q, want %q", got, typeHTML)
	}
	body := response.Body.String()
	if !strings.Contains(body, "<!doctype html>") {
		t.Error("the shell is not an HTML document")
	}
	// The application has to actually be referenced, or the page is a husk
	// that serves 200 and does nothing.
	if !strings.Contains(body, `src="/assets/app.js"`) {
		t.Error("the shell does not load the application module")
	}
}

// TestAssetsAreServedWithDeclaredContentTypes pins the type table.
//
// With nosniff set, a wrong or missing type is a page that does not work
// rather than one the browser guesses at.
func TestAssetsAreServedWithDeclaredContentTypes(t *testing.T) {
	handler := newHandler(t)

	tests := []struct {
		path string
		want string
	}{
		{"/assets/app.js", typeJS},
		{"/assets/api.js", typeJS},
		{"/assets/realtime.js", typeJS},
		{"/assets/render.js", typeJS},
		{"/assets/styles.css", typeCSS},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			response := get(t, handler, http.MethodGet, tt.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if got := response.Header().Get("Content-Type"); got != tt.want {
				t.Errorf("Content-Type = %q, want %q", got, tt.want)
			}
			if response.Body.Len() == 0 {
				t.Error("asset is empty")
			}
		})
	}
}

// TestEveryEmbeddedAssetIsReachable closes the gap between shipping a file and
// serving it.
//
// A file added to assets/ but unreachable would be dead weight in the binary;
// one served but not embedded cannot happen, since the map is built from the
// embedded set.
func TestEveryEmbeddedAssetIsReachable(t *testing.T) {
	handler := newHandler(t)
	names := assetNames()
	if len(names) == 0 {
		t.Fatal("no assets are embedded")
	}

	for _, name := range names {
		path := "/assets/" + name
		if name == "index.html" {
			path = "/"
		}
		if response := get(t, handler, http.MethodGet, path); response.Code != http.StatusOK {
			t.Errorf("embedded asset %q is not reachable at %s (status %d)",
				name, path, response.Code)
		}
	}
}

// TestHeadReturnsHeadersWithoutABody keeps HEAD honest.
func TestHeadReturnsHeadersWithoutABody(t *testing.T) {
	handler := newHandler(t)

	for _, path := range []string{"/", "/assets/app.js"} {
		t.Run(path, func(t *testing.T) {
			head := get(t, handler, http.MethodHead, path)
			body := get(t, handler, http.MethodGet, path)

			if head.Code != http.StatusOK {
				t.Fatalf("HEAD status = %d, want 200", head.Code)
			}
			if head.Body.Len() != 0 {
				t.Errorf("HEAD returned %d bytes of body", head.Body.Len())
			}
			if got, want := head.Header().Get("Content-Type"),
				body.Header().Get("Content-Type"); got != want {
				t.Errorf("HEAD Content-Type = %q, GET = %q", got, want)
			}
			// Length must describe the body a GET would return.
			if got, want := head.Header().Get("Content-Length"),
				body.Header().Get("Content-Length"); got != want {
				t.Errorf("HEAD Content-Length = %q, GET = %q", got, want)
			}
		})
	}
}

// TestUnknownPathsAreNotFound is what stops the shell from swallowing typos.
//
// A handler that answered "/" for everything would make a mistyped path look
// like a working page — and, once composed with the API, a mistyped API route
// would return 200 full of markup.
func TestUnknownPathsAreNotFound(t *testing.T) {
	handler := newHandler(t)

	paths := []string{
		"/nope",
		"/assets/",
		"/assets/missing.js",
		"/assets/app.js.map",
		"/index.html", // the shell is at "/" only
		"/v1/projects",
		"/favicon.ico",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response := get(t, handler, http.MethodGet, path)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", response.Code)
			}
			if strings.Contains(response.Body.String(), "<!doctype html>") {
				t.Error("an unknown path returned the application shell")
			}
		})
	}
}

// TestTraversalAttemptsCannotEscapeTheEmbeddedSet covers the classic shapes.
//
// The handler resolves a map key rather than a filesystem path, so there is
// nothing for these to escape into — this proves that property instead of
// assuming it.
func TestTraversalAttemptsCannotEscapeTheEmbeddedSet(t *testing.T) {
	handler := newHandler(t)

	paths := []string{
		"/assets/../handler.go",
		"/assets/../../go.mod",
		"/../go.mod",
		"/assets/%2e%2e/handler.go",
		"/assets/..%2fhandler.go",
		"/assets/....//handler.go",
		`/assets/..\handler.go`,
		"//etc/passwd",
		"/assets//etc/passwd",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil)
			// Set the raw path directly: NewRequest would clean some of these
			// before the handler ever saw them, which would test net/http
			// rather than this handler.
			parsed := request.URL
			parsed.Path = path
			parsed.RawPath = path

			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code == http.StatusOK {
				t.Fatalf("status 200 for %q; body: %.120q", path, recorder.Body.String())
			}
			body := recorder.Body.String()
			for _, leak := range []string{"package webui", "module trustvian-platform", "root:"} {
				if strings.Contains(body, leak) {
					t.Fatalf("response leaked content outside the embedded set: %.120q", body)
				}
			}
		})
	}
}

// TestNoDirectoryListing keeps the embedded tree private.
func TestNoDirectoryListing(t *testing.T) {
	handler := newHandler(t)

	for _, path := range []string{"/assets", "/assets/", "/."} {
		response := get(t, handler, http.MethodGet, path)
		if response.Code == http.StatusOK {
			t.Errorf("%s returned 200; a listing must not be served", path)
		}
		if strings.Contains(response.Body.String(), "app.js") {
			t.Errorf("%s leaked a directory listing", path)
		}
	}
}

// TestMutatingMethodsAreRefused keeps the asset space read-only.
func TestMutatingMethodsAreRefused(t *testing.T) {
	handler := newHandler(t)

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		t.Run(method, func(t *testing.T) {
			response := get(t, handler, method, "/")
			if response.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", response.Code)
			}
			if got := response.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("Allow = %q, want %q", got, "GET, HEAD")
			}
		})
	}
}

// ---------------------------------------------------------------------
// Security headers
// ---------------------------------------------------------------------

// TestSecurityHeadersOnEveryResponse includes the error paths.
//
// A policy that only covers the success path has a hole in it: a 404 is still a
// page the browser renders.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	handler := newHandler(t)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"shell", http.MethodGet, "/"},
		{"asset", http.MethodGet, "/assets/app.js"},
		{"head", http.MethodHead, "/"},
		{"not found", http.MethodGet, "/nope"},
		{"method not allowed", http.MethodPost, "/"},
	}

	want := map[string]string{
		"Content-Security-Policy": contentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
		"X-Frame-Options":         "DENY",
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			response := get(t, handler, tt.method, tt.path)
			for header, expected := range want {
				if got := response.Header().Get(header); got != expected {
					t.Errorf("%s = %q, want %q", header, got, expected)
				}
			}
		})
	}
}

// TestContentSecurityPolicyForbidsEveryEscapeHatch is the load-bearing header
// assertion.
//
// Each token below has been the reason a CSP did nothing in some real
// application. A policy naming them would still look like a policy, which is
// exactly why this is asserted rather than trusted.
func TestContentSecurityPolicyForbidsEveryEscapeHatch(t *testing.T) {
	policy := ContentSecurityPolicy()

	forbidden := []string{
		"unsafe-inline",
		"unsafe-eval",
		"unsafe-hashes",
		"*",
		"http:",
		"https:",
		"data:",
		"blob:",
		"filesystem:",
		"cdn",
		"googleapis",
		"unpkg",
		"jsdelivr",
	}
	for _, token := range forbidden {
		if strings.Contains(policy, token) {
			t.Errorf("policy contains %q: %s", token, policy)
		}
	}

	// Deny by default, then allow only same-origin fetches.
	required := []string{
		"default-src 'none'",
		"script-src 'self'",
		"style-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'self'",
	}
	for _, directive := range required {
		if !strings.Contains(policy, directive) {
			t.Errorf("policy is missing %q: %s", directive, policy)
		}
	}
}

// TestNoCORSHeaderIsEverSent is the one header that would change the security
// model.
//
// The UI is same-origin with the API, so nothing here needs CORS. Sending it
// anyway is the single change that would let any page a developer visits reach
// an unauthenticated local control plane.
func TestNoCORSHeaderIsEverSent(t *testing.T) {
	handler := newHandler(t)

	for _, path := range []string{"/", "/assets/app.js", "/nope"} {
		response := get(t, handler, http.MethodGet, path)
		for header := range response.Header() {
			if strings.HasPrefix(http.CanonicalHeaderKey(header), "Access-Control-") {
				t.Errorf("%s sent %s", path, header)
			}
		}
	}
}

// TestAssetPathsReportsWhatIsServed keeps the exported list honest, since the
// runtime's routing test enumerates from it.
func TestAssetPathsReportsWhatIsServed(t *testing.T) {
	handler := newHandler(t)
	paths := AssetPaths()
	if len(paths) < 2 {
		t.Fatalf("AssetPaths() = %v, want the shell plus assets", paths)
	}

	var sawRoot bool
	for _, path := range paths {
		if path == "/" {
			sawRoot = true
		}
		if response := get(t, handler, http.MethodGet, path); response.Code != http.StatusOK {
			t.Errorf("AssetPaths() lists %q but it returns %d", path, response.Code)
		}
	}
	if !sawRoot {
		t.Error(`AssetPaths() does not include "/"`)
	}
}

// TestContentTypeTableRefusesUnknownKinds documents the construction-time
// failure, so an asset kind nobody chose a type for cannot ship with a guess.
func TestContentTypeTableRefusesUnknownKinds(t *testing.T) {
	if _, known := contentTypeFor("assets/data.bin"); known {
		t.Error("an unknown extension was given a content type")
	}
	for name, want := range map[string]string{
		"a.html": typeHTML, "a.js": typeJS, "a.css": typeCSS,
	} {
		got, known := contentTypeFor(name)
		if !known || got != want {
			t.Errorf("contentTypeFor(%q) = %q,%v; want %q,true", name, got, known, want)
		}
	}
}
