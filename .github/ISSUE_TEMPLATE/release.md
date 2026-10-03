---
name: Release
about: Announce and track one release (docs/release-runbook.md § 7)
title: "Release vX.Y.Z"
---

<!--
One open issue per release, created before `make release-prep`.
The owner is the only person (or agent, on the owner's behalf) who runs
`make release` until this issue is closed. Hand over by reassigning it.
Procedure: docs/release-runbook.md
-->

Owner: @you · Type: minor · Candidates: yes/no

- [ ] release-prep PR: #___ (reviewed by @___)
- [ ] dry run: <run link>
- [ ] rc.1: <release link> — shared with ___     (S3 only)
- [ ] release: <release link> (approved by @___)
- [ ] verified (§ 6)
- [ ] follow-ups PR: #___
- [ ] announced

During candidates, main accepts only fixes for this release.
