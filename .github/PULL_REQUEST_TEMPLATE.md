## What this changes and why

Link the issue and explain the reason for the change.

Issue:

## Type of change

- [ ] `feat`
- [ ] `fix`
- [ ] `test`
- [ ] `docs`
- [ ] `chore`
- [ ] `refactor`

## Checklist

- [ ] `make lint` and `make test` pass locally
- [ ] `make test-e2e` run against kind when touching `internal/check` or `internal/diagnose`; otherwise marked N/A with a reason
- [ ] Unit tests added or updated, including the no-finding path and the degradation path for a new check
- [ ] Every new finding carries a concrete remediation or `ContextDependent: true`
- [ ] Ambiguous evidence produces a `*-undetermined` cause, not a guess
- [ ] Documentation updated (`README.md`, `docs/`, `rules/`) if behavior or conventions changed
- [ ] All commits signed off (`git commit -s`)
