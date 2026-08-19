# Test conventions

- **Every check in `internal/check` has unit tests using
  `client-go/kubernetes/fake`**, without exception (a project quality
  criterion). No check is merged without them.
- **External `_test` package** (`package check_test`, not `package check`):
  tests must use the package's public API as a real caller
  (`cmd/kubectl-safe_rollout`) would, so an internal refactor that breaks the
  external ergonomics is immediately visible.
- **Test names are descriptive English phrases**:
  `TestPDBConsistency_MaxUnavailableZero_High`, not `TestCase1`. They must
  remain readable as a specification when the implementation changes.
- **One test for every emitted severity and one for the "no finding" path**:
  a check with tests only for the positive case does not prove that it can
  remain silent when everything is fine (false positives cost as much
  credibility as false negatives).
- **One test for degradation**: if a check queries a resource that may be
  unavailable (RBAC, metrics-server), simulate the error with a reactor on
  `fake.Clientset` (`PrependReactor`) and verify `Result.Skipped`, not only the
  happy path.
- **Minimum coverage**: the threshold is enforced in CI; see
  `.github/workflows/ci.yml` for the current value. The build fails below the
  threshold. Raise the threshold only, and only when new code genuinely
  exceeds it (do not lower it to make CI pass).
- **E2E on kind**: one scenario for each cause classified by `watch` (a project
  quality criterion), in `test/e2e/`. They are isolated from the rest of the
  suite with the `e2e` build tag: they do not run in `go test ./...` or in CI
  (no cluster is available there), only with `make test-e2e` against an active
  kind cluster (`make kind-up`). All 19 scenarios in the suite pass on kind
  v0.32 against Kubernetes v1.36.1, v1.35.5 and v1.34.8 (containerd 2.3.1): 16 failure scenarios cover the classified causes,
  and one successful slow-start regression guards against readiness false
  positives. Each scenario creates a disposable namespace
  and deletes it at the end of the test (`t.Cleanup`); always use real,
  redirectable image/registry references (Docker Hub, non-resolving DNS) to
  exercise real containerd/kubelet/scheduler error messages, never a
  simulation.
- **Diagnose**: every determined CauseID and every `*-undetermined` fallback
  requires realistic Pod, ContainerStatus, Event, and ReplicaSet/PVC fixtures
  where relevant, using the fake clientset.
- **Watch loop**: test Pod and Deployment events separately. The latter is
  required for `ProgressDeadlineExceeded`, which can change without any Pod
  event.
