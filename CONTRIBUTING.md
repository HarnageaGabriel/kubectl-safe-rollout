# Contributing to kubectl safe-rollout

`kubectl safe-rollout` is a kubectl plugin that helps operators understand why a rollout failed or stalled and how to fix it. The most useful contributions improve cluster-aware checks, deterministic diagnoses, test coverage, documentation, or verification across supported Kubernetes environments.

## Before you start

For anything larger than a typo, open an issue before writing code so the design and scope can be agreed first.

This project deliberately refuses some features: no dashboard or TUI, no SaaS, no cost reporting, no LLM-based diagnosis, and no cluster mutation or `--apply-fix`. It is not a replacement for Argo Rollouts or Flagger. A feature pull request that was not discussed may be rejected on scope grounds, regardless of implementation quality.

## Development setup

Go 1.26 or later is required.

```shell
make build
make test
make lint
make cover
```

End-to-end tests require Docker, kind, and a running kind cluster:

```shell
make kind-up
make test-e2e
make kind-down
```

## Code conventions

Detailed conventions live in [`rules/`](rules/):

- [`rules/go.md`](rules/go.md) covers Go structure and implementation conventions.
- [`rules/test.md`](rules/test.md) covers unit, coverage, and end-to-end testing.
- [`rules/output.md`](rules/output.md) covers human-readable and JSON output.
- [`rules/commit.md`](rules/commit.md) covers commit types, scopes, and messages.
- [`rules/license.md`](rules/license.md) covers Apache License 2.0 headers and licensing requirements.

Reviewers will always check two rules:

1. A new check or diagnoser needs unit tests using `k8s.io/client-go/kubernetes/fake`, including the no-finding path and the degradation path. Checks must return `check.Skip(id, reason)` rather than an error when required cluster data is inaccessible.
2. Every `Finding` must provide concrete `Remediation.Commands` or set `Remediation.ContextDependent: true`.

All matching against `Event.Message` belongs in `internal/diagnose/pattern`. Diagnosers must prefer structured fields such as `Reason`, `ExitCode`, and `PersistentVolumeClaim.Status.Phase`.

End-to-end scenarios belong in `test/e2e/`, behind the `e2e` build tag. They require a running kind cluster and are not run in CI.

## The determinism rule

Classification must be deterministic. If evidence confirms that a rollout is stuck but cannot distinguish between known causes in a category, report a `*-undetermined` cause ID, set `Finding.Undetermined = true`, and list the observed evidence. Never guess the most probable cause and present it as certain. A wrong suggestion on a production cluster costs more than silence.

The tool never modifies cluster state and never executes remediation commands.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/) with one of these types: `feat`, `fix`, `test`, `docs`, `chore`, or `refactor`. Use the package name without the `internal/` prefix as the scope.

Write commit messages in English and use the imperative mood. The body explains why, not what. For a fix to a false positive or false negative, the commit body must name the concrete scenario that exposed it.

## Sign your commits (DCO)

Every commit must contain a `Signed-off-by: Name <email>` trailer. Add it with:

```shell
git commit -s
```

This sign-off follows the [Developer Certificate of Origin](https://developercertificate.org/) and certifies that you have the right to submit the contribution under the project license. CI rejects pull requests containing unsigned commits.

Commits authored by a bot are exempt. The certificate is a statement by a person about code they are contributing, and an automated dependency bump makes no such claim; requiring it would only make those pull requests permanently unmergeable.

If the last commit is missing its sign-off:

```shell
git commit --amend -s --no-edit
```

For several commits, sign them during a rebase:

```shell
git rebase --signoff <base>
```

## Pull request process

1. Work from a fork or branch and keep it up to date with `main`.
2. Run `make lint` and `make test` locally before opening the pull request.
3. Run `make test-e2e` when the change touches `internal/check` or `internal/diagnose`.
4. Fill in the pull request template and link the issue agreed before implementation.
5. Ensure every CI check is green before merge.

Direct pushes to `main` are not possible. The branch is protected, and every change goes through a pull request, including changes by the maintainer.

## Good places to help

Current verification gaps include:

- Only Kubernetes v1.36 has been verified end to end; the other two supported minor versions have not.
- Only containerd 2.2 has been verified. CRI-O has never been exercised, so its event-message patterns are unconfirmed.
- The HTTP 410 / etcd-compaction relist path has only been exercised against a fake clientset, never against a real `resourceVersion` expiry.
- The plugin has never been run with a namespace-scoped, read-only ServiceAccount under restricted RBAC.
- `watch` currently supports Deployment only, not StatefulSet.

## Reporting bugs and security issues

Use the repository issue templates for bug reports and feature proposals. Report vulnerabilities privately as described in [`SECURITY.md`](SECURITY.md); do not open a public issue.

## Code of Conduct

Participation in this project is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## License

Contributions are accepted under the [Apache License 2.0](LICENSE).
