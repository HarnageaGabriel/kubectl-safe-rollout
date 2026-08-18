# Security Policy

## Supported versions

This project is pre-1.0. Only the latest released version receives security fixes.

## Reporting a vulnerability

Report vulnerabilities through [GitHub private security advisories](https://github.com/HarnageaGabriel/kubectl-safe-rollout/security/advisories/new). Do not open a public issue for a vulnerability.

If private advisories are unavailable, contact gabriel.harnagea06@gmail.com.

The best-effort target for acknowledging a report is 7 days. This is a single-maintainer personal project, not a vendor-backed product with a service-level agreement.

## Scope

The plugin only reads from the cluster and never mutates cluster state. It does, however, print cluster data to stdout and in JSON output, including event messages, container names, image references, and previous container logs.

Any vulnerability that causes the plugin to disclose more data than the invoking user is authorized to read, or allows the plugin to be misused as a confused deputy, is in scope.

Any path that could make the tool automatically execute a suggested command is also in scope. The tool must never execute remediation commands automatically.
