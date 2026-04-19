# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities privately via **GitHub's private vulnerability reporting** on this repository (Security tab → "Report a vulnerability"). Do not open a public issue for security reports.

If you cannot use GitHub's private reporting, open a minimal public issue asking for a private channel; a maintainer will follow up.

## What to include

- A clear description of the issue and its impact.
- Reproduction steps or a proof-of-concept.
- The affected version (git SHA or release tag).
- Your disclosure timeline expectations.

## Response

We aim to acknowledge reports within **5 business days** and to provide an initial assessment within **10 business days**. Fixes are released as soon as a reviewed patch is ready; we will coordinate disclosure timing with reporters for non-trivial issues.

No bug bounty is offered.

## Scope

This policy covers the code in this repository. Issues in upstream dependencies (FastMCP, Google APIs, pydantic, etc.) should be reported to those projects directly; we will help route when helpful.

Each deployer runs their own instance against their own Google OAuth app. Compromises of a specific deployment's credentials, tokens, or infrastructure are the deployer's responsibility — see `docs/runbook.md` for rotation procedures.
