# Security Policy

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub
issues, discussions, or pull requests.**

Report them privately through GitHub's private vulnerability reporting:

1. Go to the repository's **Security** tab.
2. Choose **Report a vulnerability**.

Or open the form directly:
<https://github.com/trustvian/trustvian/security/advisories/new>

Only the maintainers can see a private report. Please include what you can
of:

- the affected component (engine, CLI, Collector processor, local control
  plane and WebUI, GitHub Actions in `.github/actions/`, container image,
  backup/restore scripts, release artifacts) and version or commit;
- the impact — what an attacker gains or what guarantee breaks;
- steps to reproduce, or a proof of concept;
- any known mitigation.

## What to expect

- An acknowledgement once a maintainer has read the report.
- An assessment of whether it is a vulnerability and how severe it is,
  followed by a fix or a documented mitigation for confirmed issues.
- Coordinated disclosure: a GitHub Security Advisory is published with the
  fix, crediting the reporter unless they prefer otherwise.

Trustvian is maintained on a best-effort basis and makes no response-time
guarantee. There is no bug bounty.

## Supported versions

Trustvian is pre-`v1.0`. Security fixes are made on the latest release line
only; upgrade to the newest release to receive them. See
[`docs/operations.md`](../docs/operations.md#upgrade) for a safe upgrade
procedure.

## Scope

In scope: this repository's code, the released binaries (`trustvian`,
`trustvian-local` and `trustvian-collector`), and the official container image
`ghcr.io/trustvian/trustvian-collector`.

Useful context before reporting:

- [`docs/SECURITY.md`](../docs/SECURITY.md) — Trustvian's threat model, the
  guarantees it makes, and what it deliberately does **not** protect against.
  A behavior documented there as out of scope is not a vulnerability, though
  a guarantee documented there that does not hold is.
- [`docs/supply-chain.md`](../docs/supply-chain.md) — how releases and images
  are built, scanned, signed, and verified, including known unfixable
  upstream advisories.
- The reference deployment under `deployments/docker-compose/` uses
  deliberate placeholder credentials and no TLS; it is documented as not
  hardened.
