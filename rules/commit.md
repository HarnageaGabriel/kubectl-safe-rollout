# Commit conventions

Conventional Commits, like much of the cloud-native ecosystem this project
aims to interact with (contributions, automated changelogs, and possible
future integration with release-please or similar tools for krew releases).

```
<type>(<optional scope>): <short imperative description in English>

<optional body: why, not what — the diff already shows what>
```

Types used in this repository:

- `feat` — new check in `check`, new classification in `diagnose`, new
  command.
- `fix` — correction to an existing check (false positive, false negative,
  incorrect remediation).
- `test` — tests only, no logic changes.
- `docs` — README, rules/, docs/.
- `chore` — dependencies, CI, scaffolding.
- `refactor` — no observable behavior change.

Recommended scopes: the affected package without `internal/` (`check`,
`workload`, `output`, `kube`, `cmd`).

Examples:

```
feat(check): add ResourceQuota headroom check
fix(check): handle nil selector as no match in pdb-consistency
test(check): add Recreate case with exhausted PDB budget
docs: document end-to-end test workflow
```

A fix for a false positive or false negative in a check **must** mention in the
body the concrete scenario that exposed it. This makes the commit useful to
someone debugging the same class of problem in the future, not merely a
changelog entry.

Every commit must include the `Signed-off-by` trailer, added with
`git commit -s`, in accordance with the Developer Certificate of Origin (DCO).
CI rejects pull requests containing commits without sign-off.
