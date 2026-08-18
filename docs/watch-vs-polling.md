# Decision: Watch API, not polling

Architectural decision finalized before implementing `watch`.

## Decision

`watch` uses the Kubernetes Watch API for the workload's Pods and
Deployment. It starts with a `List` of the Pods and a `Get` of the
Deployment, both with a `resourceVersion`, then opens two `RetryWatcher`s.

It does not use fixed-interval polling.

## Rationale

- **Responsiveness**: the apiserver reports transitions as soon as they
  happen. A polling interval would add latency and could miss brief states.
- **Load**: watches send deltas. Polling would repeat `List` even when
  nothing changes, an unnecessary cost on large clusters or namespaces.
- **ProgressDeadlineExceeded**: it is observed on the Deployment, not
  inferred from the Pods. The second stream avoids waiting for a Pod event
  that might not arrive when only `Deployment.Status` changes.
- **Limited scope**: the selector restricts Pods to the workload; the field
  selector restricts the Deployment to the requested name.

## Reconnection

`RetryWatcher` reopens the stream from the last `resourceVersion` for
recoverable errors, including EOF, timeout, and an apiserver restart. It does
not run a new List when the resourceVersion has expired: on HTTP 410 it ends
the stream with an Error event. The outer loop catches that case, repeats
List/Get, and recreates both watches from consistent snapshots.

Unrecoverable errors, including Forbidden and Unauthorized, are returned
explicitly. There is no silent fallback to polling.

## Why not SharedInformer

`watch` lives for a single rollout. It does not need a shared cache, an
indexer, or a long-running controller process. `RetryWatcher` plus an explicit
re-list on HTTP 410 provide the required semantics with less state and a
smaller error surface.

## Point-in-time reads during diagnosis

Every Pod or Deployment event triggers a reevaluation:

- `Get` of the Deployment for success and `ProgressDeadlineExceeded`;
- `List` of owned ReplicaSets, for quota-related `FailedCreate` events;
- `List` of namespace Events, correlated by UID;
- `Get` of only the PVCs referenced by Pending Pods;
- limited `--previous` logs, only for application exit codes.

ReplicaSets and Events do not have separate streams in the MVP. This choice
avoids correlating four independent streams. If measurements on large
clusters show excessive load, the first optimization will be a local Event
cache or a filtered watch, not polling.

## Time windows: stability and grace

Two mechanisms added after running the e2e scenarios on kind (`test/e2e/`),
both real bugs that surfaced only against a real apiserver and were never
visible with the fake clientset:

- **rolloutStabilityWindow (5s)**: a container without a `readinessProbe`
  counts as Ready as soon as the kubelet makes it Running, even for an instant
  before it crashes. Reading `RolloutComplete()` only once could therefore
  declare success for a rollout that entered `CrashLoopBackOff` a moment
  later. Now the "complete" reading must remain true continuously for the
  duration of the window before success is final.
- **undeterminedGraceWindow (3s) + gracePollInterval (500ms)**: a Pod in
  `ImagePullBackOff` can show that state before the `Reason=Failed` Event with
  the detailed message is visible through `List`. Stopping at the first tick
  would have reported "undetermined" even when the specific cause was about
  to arrive. When every Finding from the tick is `Undetermined`, `watch`
  repeats `evaluateTick` (which rereads Deployment/ReplicaSet/Event) every
  `gracePollInterval` until a determined cause emerges or
  `undeterminedGraceWindow` expires. At that point, "undetermined" is the
  honest outcome, no longer a premature reading.

`gracePollInterval` is the only declared exception to this document's
push-driven principle: it is active only while a cause remains ambiguous,
never beyond `undeterminedGraceWindow`. It is not a drift toward polling as
the primary mechanism. The trigger that causes `watch` to stop remains a real
event (Pod, Deployment, or the cause becoming more specific), not a timer
deciding on its own.

Both windows can be overridden for tests (`WatchTarget.StabilityWindow`,
`WatchTarget.UndeterminedGraceWindow`): `cmd/kubectl-safe_rollout` never sets
them, so real usage always gets the production defaults.

## Current verification limits

The fake clientsets verify Pod/Deployment transitions, classification, and
Watch API wiring. They do not faithfully simulate etcd compaction, TCP
reconnections, real HTTP 410 responses, high load, or runtime variations in
Event messages. These cases require real clusters and kind tests.
