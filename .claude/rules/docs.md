# Documentation Conventions

## No machine-identifying data

Documentation is published. Real command output pasted into it must not
carry anything that identifies the machine or the person who ran it:

- home directories (`/Users/<name>`, `/home/<name>`, `C:\Users\<name>`) —
  write `~/...`, or a placeholder such as `/Users/you` when the output
  really is an absolute path;
- macOS per-user temp directories (`/var/folders/...`);
- hostnames, local usernames, email addresses, machine serials, MAC or
  private IP addresses beyond `127.0.0.1`/`localhost`, and access tokens.

Anonymize when you paste, not afterwards: keep the shape of the output
(the reader still needs to see an absolute path, a hash, a port) and
replace only the identifying part. `/home/runner/...` is allowed — it is
the GitHub-hosted runner's account, not a person.

`TestMarkdownCarriesNoMachinePaths` (`scripts/doc_privacy_test.go`) fails on
any home or temp path in tracked Markdown. It cannot catch hostnames or
usernames outside a path — those are on you.
