//go:build windows

package evaluation

// syncDirectory is a no-op on Windows.
//
// There is no portable way to obtain a writable handle to a directory and
// flush its metadata: os.Open on a directory yields a handle FlushFileBuffers
// refuses, so the call would fail on every write rather than making anything
// durable. Returning an error instead would make the journal unusable on
// Windows without making it safer.
//
// So the guarantee is stated honestly rather than claimed: on Windows this
// journal survives a process dying — the case it exists for, and the one that
// covers an OOM kill, a panic or a container stop — but a host that loses
// power may come back having lost the most recent rename or unlink. An
// evaluation-configured Collector whose host crashes on Windows can therefore
// hit the indeterminate case (ADR 0038 §10) where a POSIX host would not.
func syncDirectory(string) error { return nil }
