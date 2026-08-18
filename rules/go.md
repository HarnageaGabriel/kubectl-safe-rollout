# Go conventions

- **Module**: `github.com/HarnageaGabriel/kubectl-safe-rollout`. Always use
  absolute import paths from here, never relative ones.
- **`internal/` is an architectural boundary, not just a Go convention**:
  `internal/check`, `internal/diagnose`, `internal/remediate`,
  `internal/workload`, and `internal/model` do not import `github.com/
  spf13/cobra`, call `os.Exit`, or write directly to stdout/stderr. Only
  `cmd/kubectl-safe_rollout/` and `internal/output/` may do so: the boundary is
  deliberately clean, so a future promotion to `pkg/` (for an Action or an
  admission controller) remains a mechanical move, not a refactor.
- **Interfaces for clients**, not concrete types: functions that communicate
  with the cluster accept `kubernetes.Interface` /
  `metricsv1beta1.MetricsV1beta1Interface`, never `*kubernetes.Clientset`.
  This is what makes `client-go/kubernetes/fake` usable in tests without
  additional wrappers.
- **Degrade, do not panic**: if a resource is not accessible (insufficient
  RBAC, metrics-server unavailable), the check returns
  `check.Skip(id, reason)`, not an error that stops the entire `check` run. An
  error from `Check.Run` is reserved for internal bugs (for example, building
  a selector failed because of a bug in our code), not expected cluster
  conditions.
- **Isolated Event parsing**: all string matching on `Event.Message` lives in
  `internal/diagnose/pattern`. Diagnosers use structured fields first
  (`Reason`, `ExitCode`, `PVC.Status.Phase`) and fall back to
  `*-undetermined` if no pattern matches.
- **No silent defaults for values the brief requires to be visible**: if a
  Kubernetes default is explicitly applied in code (for example, `Replicas`
  nil -> 1, unset `MaxUnavailable` -> 25%), comment on why, as in
  [internal/workload/workload.go](../internal/workload/workload.go).
- **Errors**: `fmt.Errorf("action: %w", err)` with the action in lowercase
  English, consistent with the rest of the user-facing messages (see
  `rules/output.md`). Always wrap; never lose the original error.
- **Clean `gofmt` and `go vet` before every commit**: CI fails otherwise (see
  `.github/workflows/ci.yml`).
- **No `pkg/` until it is genuinely needed**: do not anticipate promotion to
  a public library by moving code prematurely. The restriction against
  importing `cobra` in `internal/` is enough to keep that option open.
