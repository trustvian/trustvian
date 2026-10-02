package main

// Reading a trustvian-run artifact directory.
//
// The directory is untrusted: it was produced by a job that ran the pull
// request's own code. Only its two fixed file names are opened, each must be
// a regular file (never a symbolic link, which would be a path the artifact
// chose), each is read under a size bound, and nothing in them is executed or
// followed. The metadata's digest and size are checked against the bytes
// actually read — which proves the two files agree with each other, not that
// either is authentic.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// The artifact's fixed file names (docs/ci-github-action.md).
const (
	metadataFile = "trustvian-run.json"
	resultFile   = "result.json"
)

// Size bounds. A result is at most what the run action itself preserves: the
// CLI's 32 MiB suite cap plus 64 KiB of framing. The metadata is a few hundred
// bytes; 64 KiB leaves room for additive fields and nothing more.
const (
	maxResultBytes   = 32<<20 + 64<<10
	maxMetadataBytes = 64 << 10
)

// errMissing reports that a fixed artifact file does not exist.
var errMissing = errors.New("missing")

// readBounded reads one fixed-name file from dir, refusing anything but a
// regular file and anything over limit bytes.
func readBounded(dir, name string, limit int64) ([]byte, error) {
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", name, errMissing)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: cannot be examined", name)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s: is not a regular file", errSchema, name)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: cannot be opened", name)
	}
	defer f.Close()
	// The file opened must be the one examined: a name swapped for a link
	// between the two calls is refused.
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s: changed while it was opened", errSchema, name)
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: cannot be read", name)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%w: %s: is larger than %d bytes", errSchema, name, limit)
	}
	return raw, nil
}

// exists reports whether name is present in dir at all, as any kind of entry.
func exists(dir, name string) bool {
	_, err := os.Lstat(filepath.Join(dir, name))
	return err == nil
}

// The metadata's closed vocabularies, as run.sh writes them.
const (
	statusPresent   = "present"
	statusAbsent    = "absent"
	statusOversized = "oversized"
	statusInvalid   = "invalid"

	modeScenario = "scenario"
	modeSuite    = "suite"
	modeBoth     = "both"
	modeNone     = "none"
)

var (
	// shaText is a commit in either object format, SHA-1 or SHA-256, exactly
	// as the run action accepts one (lib.sh, resolve_head).
	shaText    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	sha256Text = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// metadata is trustvian-run.json, version "1".
type metadata struct {
	mode         string
	exitCode     int64
	resultStatus string
	resultBytes  int64
	resultSHA256 string
}

// parseMetadata validates trustvian-run.json and checks every identity it
// records against what the caller expects. A mismatch is a rejection: the
// artifact does not describe this commit, repository, run or exit code.
func parseMetadata(raw []byte, want expectedContext) (metadata, error) {
	var m metadata
	doc, err := parseStrict(raw)
	if err != nil {
		return m, err
	}
	if doc.kind != kindObject {
		return m, schemaErr(metadataFile, "is not an object")
	}
	if err := equalText(doc, "", "version", "1"); err != nil {
		return m, err
	}
	head, err := object(doc, "", "head")
	if err != nil {
		return m, err
	}
	sha, err := str(head, "head", "sha")
	if err != nil {
		return m, err
	}
	if !shaText.MatchString(sha) {
		return m, schemaErr("head.sha", "is not a lowercase hexadecimal commit SHA")
	}
	if sha != want.headSHA {
		return m, schemaErr("head.sha", "is not the expected head commit")
	}
	if _, err := enum(head, "head", "source", "event.pull_request.head.sha", "github.sha"); err != nil {
		return m, err
	}
	if _, err := str(doc, "", "event"); err != nil {
		return m, err
	}
	if repo, err := str(doc, "", "repository"); err != nil {
		return m, err
	} else if repo != want.repository {
		return m, schemaErr("repository", "is not the expected repository")
	}
	run, err := object(doc, "", "run")
	if err != nil {
		return m, err
	}
	if id, err := str(run, "run", "id"); err != nil {
		return m, err
	} else if id != want.runID {
		return m, schemaErr("run.id", "is not the expected run")
	}
	if attempt, err := str(run, "run", "attempt"); err != nil {
		return m, err
	} else if attempt != want.runAttempt {
		return m, schemaErr("run.attempt", "is not the expected run attempt")
	}
	if m.mode, err = enum(doc, "", "mode", modeScenario, modeSuite, modeBoth, modeNone); err != nil {
		return m, err
	}
	if _, err := enum(doc, "", "control_plane", "started", "attached"); err != nil {
		return m, err
	}
	cli, err := object(doc, "", "cli")
	if err != nil {
		return m, err
	}
	// Any status the process can exit with; whether it is in the CLI's
	// contract is decided later, against the expected code.
	if m.exitCode, err = integer(cli, "cli", "exit_code", 0, 255); err != nil {
		return m, err
	}
	if m.exitCode != int64(*want.exitCode) {
		return m, schemaErr("cli.exit_code", "is not the exit code the run job reported")
	}
	runtime, err := object(doc, "", "runtime")
	if err != nil {
		return m, err
	}
	if _, err := str(runtime, "runtime", "source_commit"); err != nil {
		return m, err
	}
	if _, err := str(runtime, "runtime", "go_version"); err != nil {
		return m, err
	}

	result, err := object(doc, "", "result")
	if err != nil {
		return m, err
	}
	if m.resultStatus, err = enum(result, "result", "status",
		statusPresent, statusAbsent, statusOversized, statusInvalid); err != nil {
		return m, err
	}
	if m.resultStatus == statusPresent {
		// The artifact's own file name is checked, never used: the file read
		// is always result.json.
		if err := equalText(result, "result", "file", resultFile); err != nil {
			return m, err
		}
		if m.resultBytes, err = integer(result, "result", "bytes", 2, maxResultBytes); err != nil {
			return m, err
		}
		if m.resultSHA256, err = str(result, "result", "sha256"); err != nil {
			return m, err
		}
		if !sha256Text.MatchString(m.resultSHA256) {
			return m, schemaErr("result.sha256", "is not a 64-character lowercase SHA-256")
		}
	} else {
		for _, name := range []string{"file", "bytes", "sha256"} {
			if _, ok := result.fields[name]; ok {
				return m, schemaErr(join("result", name), "is present although no result was preserved")
			}
		}
	}
	return m, nil
}

// checkDigest confirms result.json is the file the metadata describes.
func checkDigest(raw []byte, m metadata) error {
	if int64(len(raw)) != m.resultBytes {
		return schemaErr(resultFile, "is not the size trustvian-run.json records")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != m.resultSHA256 {
		return schemaErr(resultFile, "does not match the SHA-256 trustvian-run.json records")
	}
	return nil
}
