package main

// Task 062: resolving the local runtime endpoint.
//
// The resolver is the one place a working directory can influence where a
// command goes, so most of this file is about what it must refuse.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeDiscoveryFile plants a runtime file in the current directory.
func writeDiscoveryFile(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(localStateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(localStateDir, localDiscoveryFile)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func discoveryFor(apiURL string) string {
	return fmt.Sprintf(`{"version":"1","api_url":%q}`, apiURL)
}

// TestDiscoveryResolvesLocalRuntime covers the ordinary local path.
func TestDiscoveryResolvesLocalRuntime(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, `{"version":"1","id":"proj-1","name":"Checkout"}`)

	t.Chdir(t.TempDir())
	writeDiscoveryFile(t, discoveryFor(api.url()))

	result := runPlatformCLI(t, "project", "get", "--id", "proj-1")
	result.mustExit(t, exitOK, "project get through discovery")

	if got := api.only().escapedPath; got != "/v1/projects/proj-1" {
		t.Errorf("path = %q", got)
	}
	if !strings.Contains(result.stdout, "proj-1") {
		t.Errorf("stdout did not render the project:\n%s", result.stdout)
	}
}

// TestDiscoveryWorksForMutations proves writes resolve too, not just reads.
func TestDiscoveryWorksForMutations(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(201, `{"version":"1","id":"proj-1","name":"Checkout"}`)

	t.Chdir(t.TempDir())
	writeDiscoveryFile(t, discoveryFor(api.url()))

	runPlatformCLI(t, "project", "create", "--id", "proj-1", "--name", "Checkout").
		mustExit(t, exitOK, "project create through discovery")

	request := api.only()
	if request.method != "POST" || request.escapedPath != "/v1/projects" {
		t.Errorf("request = %s %s", request.method, request.escapedPath)
	}
}

// TestExplicitAPIURLOverridesDiscovery is the property CI depends on.
//
// A checked-out working directory must never be able to redirect a command
// that named its own endpoint.
func TestExplicitAPIURLOverridesDiscovery(t *testing.T) {
	discovered := newFakeAPI(t)
	discovered.reply(200, `{"version":"1","id":"wrong","name":"Discovered"}`)

	explicit := newFakeAPI(t)
	explicit.reply(200, `{"version":"1","id":"proj-1","name":"Explicit"}`)

	t.Chdir(t.TempDir())
	writeDiscoveryFile(t, discoveryFor(discovered.url()))

	runPlatformCLI(t, "project", "get", "--id", "proj-1", "--api-url", explicit.url()).
		mustExit(t, exitOK, "explicit wins")

	if got := len(discovered.captured()); got != 0 {
		t.Fatalf("the discovered endpoint received %d requests; explicit input must win", got)
	}
	if got := len(explicit.captured()); got != 1 {
		t.Fatalf("the explicit endpoint received %d requests, want 1", got)
	}
}

// TestDiscoveryMayOnlyPointAtLoopback is the security property.
//
// A repository can contain .trustvian/runtime.json. If a discovered URL could
// be anything, checking out a project would be enough to send a developer's
// mutation commands to someone else's server.
func TestDiscoveryMayOnlyPointAtLoopback(t *testing.T) {
	tests := []struct{ name, apiURL string }{
		{"remote https", "https://example.com"},
		{"remote http", "http://example.com"},
		{"private range", "http://192.168.1.10:8080"},
		{"other private range", "http://10.0.0.5:8080"},
		{"all interfaces", "http://0.0.0.0:8080"},
		{"hostname that may resolve to loopback", "http://localhost:8080"},
		{"credentials", "http://user:pass@127.0.0.1:8080"},
		{"https on loopback", "https://127.0.0.1:8080"},
		{"path", "http://127.0.0.1:8080/api"},
		{"query", "http://127.0.0.1:8080?a=b"},
		{"fragment", "http://127.0.0.1:8080#f"},
		{"no port", "http://127.0.0.1"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A live server the malicious file could reach if validation let
			// it through — so "zero requests" is a real observation.
			decoy := newFakeAPI(t)
			decoy.reply(200, `{"version":"1","id":"owned"}`)

			t.Chdir(t.TempDir())
			writeDiscoveryFile(t, discoveryFor(tt.apiURL))

			result := runPlatformCLI(t, "project", "get", "--id", "proj-1")
			result.mustExit(t, exitOperational, tt.name)

			if len(decoy.captured()) != 0 {
				t.Fatal("a request escaped to a non-loopback endpoint")
			}
			if result.stdout != "" {
				t.Errorf("stdout = %q, want empty", result.stdout)
			}
			if len(result.stderr) > 4096 {
				t.Errorf("diagnostic is %d bytes; it should stay bounded", len(result.stderr))
			}
		})
	}
}

// TestDiscoveryFileIsSizeBounded checks the boundary from both sides.
func TestDiscoveryFileIsSizeBounded(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, `{"version":"1","id":"proj-1","name":"Checkout"}`)

	build := func(total int) string {
		base := fmt.Sprintf(`{"version":"1","api_url":%q,"pad":"`, api.url())
		suffix := `"}`
		padding := total - len(base) - len(suffix)
		if padding < 0 {
			t.Fatalf("total %d too small", total)
		}
		content := base + strings.Repeat("x", padding) + suffix
		if len(content) != total {
			t.Fatalf("built %d bytes, want %d", len(content), total)
		}
		return content
	}

	t.Run("at the limit is accepted", func(t *testing.T) {
		t.Chdir(t.TempDir())
		writeDiscoveryFile(t, build(localDiscoveryLimit))
		runPlatformCLI(t, "project", "get", "--id", "proj-1").
			mustExit(t, exitOK, "at the limit")
	})

	t.Run("one byte over is refused", func(t *testing.T) {
		before := len(api.captured())
		t.Chdir(t.TempDir())
		writeDiscoveryFile(t, build(localDiscoveryLimit+1))

		runPlatformCLI(t, "project", "get", "--id", "proj-1").
			mustExit(t, exitOperational, "over the limit")

		if len(api.captured()) != before {
			t.Fatal("a request was sent for an oversized discovery file")
		}
	})
}

// TestDiscoveryVersionHandling: additive fields grow, unknown versions fail.
func TestDiscoveryVersionHandling(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, `{"version":"1","id":"proj-1","name":"Checkout"}`)

	t.Run("additive fields tolerated", func(t *testing.T) {
		t.Chdir(t.TempDir())
		writeDiscoveryFile(t, fmt.Sprintf(
			`{"version":"1","api_url":%q,"future_field":{"x":1},"another":"y"}`, api.url()))
		runPlatformCLI(t, "project", "get", "--id", "proj-1").
			mustExit(t, exitOK, "additive fields")
	})

	for _, version := range []string{"2", "0", "", "1.0", "v1"} {
		t.Run("version "+version+" fails closed", func(t *testing.T) {
			before := len(api.captured())
			t.Chdir(t.TempDir())
			writeDiscoveryFile(t, fmt.Sprintf(
				`{"version":%q,"api_url":%q}`, version, api.url()))

			runPlatformCLI(t, "project", "get", "--id", "proj-1").
				mustExit(t, exitOperational, "version "+version)

			if len(api.captured()) != before {
				t.Fatal("a request was sent for an unsupported discovery version")
			}
		})
	}
}

// TestMalformedDiscoveryIsOperational covers the remaining broken shapes.
func TestMalformedDiscoveryIsOperational(t *testing.T) {
	for _, content := range []string{
		"not json",
		`{"version":"1"`,
		`{"version":"1"}`,
		``,
		`[]`,
	} {
		t.Run(fmt.Sprintf("%q", content), func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeDiscoveryFile(t, content)

			result := runPlatformCLI(t, "project", "get", "--id", "proj-1")
			result.mustExit(t, exitOperational, content)
			if result.stdout != "" {
				t.Errorf("stdout = %q, want empty", result.stdout)
			}
		})
	}
}

// TestTUIResolvesThroughDiscovery proves the dashboard shares the resolver.
func TestTUIResolvesThroughDiscovery(t *testing.T) {
	var sawRealtime bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/realtime") {
			sawRealtime = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	t.Chdir(t.TempDir())
	writeDiscoveryFile(t, discoveryFor(server.URL))

	// Resolution is what is under test; the model's behaviour has its own
	// tests, so this only needs to prove the endpoint was found and used.
	client, err := resolveAPIURL("", false, 2*time.Second)
	if err != nil {
		t.Fatalf("resolveAPIURL: %v", err)
	}
	if client.baseURL.String() != server.URL {
		t.Fatalf("resolved %q, want %q", client.baseURL.String(), server.URL)
	}
	_ = sawRealtime
}

// TestDiscoveryIsReadFromWorkingDirectoryOnly pins the search rule.
//
// No parent walk, no $HOME, no environment variable: a directory owns its
// runtime, and a file one level up must not silently capture commands.
func TestDiscoveryIsReadFromWorkingDirectoryOnly(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, `{"version":"1","id":"proj-1"}`)

	parent := t.TempDir()
	child := filepath.Join(parent, "nested")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Discovery in the parent only.
	if err := os.MkdirAll(filepath.Join(parent, localStateDir), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(parent, localStateDir, localDiscoveryFile),
		[]byte(discoveryFor(api.url())), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	t.Chdir(child)
	result := runPlatformCLI(t, "project", "get", "--id", "proj-1")
	result.mustExit(t, exitOperational, "parent discovery must not be found")

	if len(api.captured()) != 0 {
		t.Fatal("a parent directory's runtime file was used")
	}
}

// TestDiscoveryDecodesWithoutRejectingUnknownFields guards the decoder shape
// directly, since the behaviour above could also be satisfied by luck.
func TestDiscoveryDecodesWithoutRejectingUnknownFields(t *testing.T) {
	var discovery localDiscovery
	err := json.Unmarshal([]byte(
		`{"version":"1","api_url":"http://127.0.0.1:1","extra":{"a":[1,2]}}`), &discovery)
	if err != nil {
		t.Fatalf("additive fields must decode: %v", err)
	}
	if discovery.APIURL != "http://127.0.0.1:1" {
		t.Errorf("api_url = %q", discovery.APIURL)
	}
}

// ---------------------------------------------------------------------
// Explicit flag presence
// ---------------------------------------------------------------------

// TestExplicitEmptyAPIURLDoesNotUseDiscovery is the CI-protecting property.
//
// `--api-url ""` is almost always an unset shell variable:
//
//	trustvian eval compare --api-url "$CONTROL_PLANE_URL" …
//
// Falling back to a local runtime there would run the command against the
// wrong control plane and report a result for it. An explicitly supplied value
// is the caller's choice even when it is empty, so it stays a usage error and
// no discovery file is consulted.
func TestExplicitEmptyAPIURLDoesNotUseDiscovery(t *testing.T) {
	commands := [][]string{
		{"project", "get", "--id", "proj-1"},
		{"project", "create", "--id", "proj-1", "--name", "n"},
		{"agent", "get", "--id", "agent-1"},
		{"candidate", "get", "--id", "cand-1"},
		{"eval", "get", "--id", "run-42"},
		{"eval", "start", "--id", "run-42"},
		{"eval", "progress", "--id", "run-42"},
	}

	// Both spellings the flag package accepts.
	for _, form := range []struct {
		name string
		args []string
	}{
		{"separate argument", []string{"--api-url", ""}},
		{"equals form", []string{"--api-url="}},
	} {
		for _, command := range commands {
			t.Run(form.name+"/"+strings.Join(command[:2], " "), func(t *testing.T) {
				discovered := newFakeAPI(t)
				discovered.reply(200, `{"version":"1","id":"proj-1"}`)

				t.Chdir(t.TempDir())
				writeDiscoveryFile(t, discoveryFor(discovered.url()))

				result := runPlatformCLI(t, append(append([]string{}, command...), form.args...)...)
				result.mustExit(t, exitUsage, form.name)

				if len(discovered.captured()) != 0 {
					t.Fatal("an explicitly empty --api-url fell back to local discovery")
				}
				if result.stdout != "" {
					t.Errorf("stdout = %q, want empty", result.stdout)
				}
				if result.stderr == "" {
					t.Error("stderr is empty; a refused invocation must be diagnosed")
				}
			})
		}
	}
}

// TestCompareExplicitEmptyAPIURLIsUsage covers the command where the mistake
// would be most expensive: a gate verdict against the wrong control plane.
func TestCompareExplicitEmptyAPIURLIsUsage(t *testing.T) {
	discovered := newFakeAPI(t)
	discovered.reply(200, comparisonBody(gateVerdictPass, 0, "0"))

	t.Chdir(t.TempDir())
	writeDiscoveryFile(t, discoveryFor(discovered.url()))

	args := withoutFlag(compareArgs(""), "--api-url")
	result := runPlatformCLI(t, append(args, "--api-url", "")...)

	result.mustExit(t, exitUsage, "compare with an empty endpoint")
	if result.code == exitGateFail || result.code == exitOK {
		t.Fatal("an unset endpoint produced a gate result")
	}
	if len(discovered.captured()) != 0 {
		t.Fatal("compare fell back to the local runtime after an explicit empty --api-url")
	}
}

// TestOmittedAPIURLStillUsesDiscovery keeps the other half working: only
// explicit input suppresses the fallback.
func TestOmittedAPIURLStillUsesDiscovery(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, `{"version":"1","id":"proj-1","name":"Checkout"}`)

	t.Chdir(t.TempDir())
	writeDiscoveryFile(t, discoveryFor(api.url()))

	runPlatformCLI(t, "project", "get", "--id", "proj-1").
		mustExit(t, exitOK, "omitted --api-url")
	if len(api.captured()) != 1 {
		t.Fatalf("discovery was not used; requests = %d", len(api.captured()))
	}
}

// ---------------------------------------------------------------------
// One bounded JSON document
// ---------------------------------------------------------------------

// TestDiscoveryMustBeExactlyOneJSONDocument closes a parser gap.
//
// json.Decoder reads one value and stops, so a file whose first object is
// valid was accepted no matter what followed it — including a second object
// naming a different endpoint. Unmarshal requires the whole payload to be one
// value.
func TestDiscoveryMustBeExactlyOneJSONDocument(t *testing.T) {
	good := newFakeAPI(t)
	good.reply(200, `{"version":"1","id":"proj-1","name":"Checkout"}`)

	other := newFakeAPI(t)
	other.reply(200, `{"version":"1","id":"wrong"}`)

	tests := []struct {
		name    string
		content string
		wantOK  bool
	}{
		{"single document", discoveryFor(good.url()), true},
		{"trailing newline", discoveryFor(good.url()) + "\n", true},
		{"trailing whitespace", discoveryFor(good.url()) + "  \n\t\n", true},
		{"additive fields", fmt.Sprintf(
			`{"version":"1","api_url":%q,"future_field":{"x":1}}`, good.url()), true},

		{"second document", discoveryFor(good.url()) + "\n" + discoveryFor(other.url()), false},
		{"trailing garbage", discoveryFor(good.url()) + "garbage", false},
		{"trailing garbage after newline", discoveryFor(good.url()) + "\ngarbage", false},
		{"two documents on one line", discoveryFor(good.url()) + discoveryFor(other.url()), false},
		{"array wrapper", "[" + discoveryFor(good.url()) + "]", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(good.captured()) + len(other.captured())

			t.Chdir(t.TempDir())
			writeDiscoveryFile(t, tt.content)

			result := runPlatformCLI(t, "project", "get", "--id", "proj-1")
			if tt.wantOK {
				result.mustExit(t, exitOK, tt.name)
				return
			}

			result.mustExit(t, exitOperational, tt.name)
			if len(good.captured())+len(other.captured()) != before {
				t.Fatal("a request was sent for a discovery file that is not one document")
			}
		})
	}
}

// TestDiscoveryBoundAppliesToBytesRead proves the cap is on the payload, not
// on stat metadata.
//
// A file can grow between the size check and the read, and a decoder that
// stops after the first value would never notice. The read itself is capped.
func TestDiscoveryBoundAppliesToBytesRead(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, `{"version":"1","id":"proj-1","name":"Checkout"}`)

	build := func(total int) string {
		base := fmt.Sprintf(`{"version":"1","api_url":%q,"pad":"`, api.url())
		suffix := `"}`
		padding := total - len(base) - len(suffix)
		if padding < 0 {
			t.Fatalf("total %d too small", total)
		}
		content := base + strings.Repeat("x", padding) + suffix
		if len(content) != total {
			t.Fatalf("built %d bytes, want %d", len(content), total)
		}
		return content
	}

	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		t.Chdir(t.TempDir())
		writeDiscoveryFile(t, build(localDiscoveryLimit))
		runPlatformCLI(t, "project", "get", "--id", "proj-1").
			mustExit(t, exitOK, "at the limit")
	})

	t.Run("one byte over is refused", func(t *testing.T) {
		before := len(api.captured())
		t.Chdir(t.TempDir())
		writeDiscoveryFile(t, build(localDiscoveryLimit+1))

		runPlatformCLI(t, "project", "get", "--id", "proj-1").
			mustExit(t, exitOperational, "over the limit")
		if len(api.captured()) != before {
			t.Fatal("a request was sent for an oversized discovery file")
		}
	})

	// The case above alone does not prove the bound is enforced: truncating a
	// padded object at the limit leaves unterminated JSON, so a reader with no
	// length check would still refuse it — for a parse error, not for its
	// size. Padding with whitespace instead makes the truncated payload parse
	// cleanly, so only an actual comparison of bytes read can reject it.
	t.Run("oversized whitespace padding is refused, not silently truncated", func(t *testing.T) {
		before := len(api.captured())
		t.Chdir(t.TempDir())
		writeDiscoveryFile(t,
			discoveryFor(api.url())+strings.Repeat(" ", localDiscoveryLimit))

		result := runPlatformCLI(t, "project", "get", "--id", "proj-1")
		result.mustExit(t, exitOperational, "oversized with whitespace padding")
		if len(api.captured()) != before {
			t.Fatal("an oversized discovery file was truncated and used")
		}
		if !strings.Contains(result.stderr, "limit") {
			t.Errorf("stderr does not name the size bound: %q", result.stderr)
		}
	})
}
