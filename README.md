# kubectl safe-rollout

A `kubectl` plugin that shortens the time between "the deploy failed" and "I know why and how to fix it".

[![CI](https://github.com/HarnageaGabriel/kubectl-safe-rollout/actions/workflows/ci.yml/badge.svg)](https://github.com/HarnageaGabriel/kubectl-safe-rollout/actions/workflows/ci.yml) [![Go version](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](https://go.dev/) [![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

## What it does

- `kubectl safe-rollout check <kind>/<name>` — pre-flight analysis of a live workload and the state of its namespace.
- `kubectl safe-rollout watch <kind>/<name>` — watches a rollout through the Kubernetes Watch API and, when it fails or stalls, classifies the cause and proposes remediation based on the evidence it collected.

`<kind>` accepts `deployment` or `statefulset` (and their usual short/plural aliases, e.g. `deploy`, `sts`). StatefulSet support has been added to both commands, but is e2e-verified against a real cluster only for Deployment so far: StatefulSet e2e scenarios exist in `test/e2e/statefulset_test.go` and have not yet been run on a cluster (see "Verified and not verified" below).

Three properties are worth knowing before you install it:

1. **It never modifies the cluster.** It is read-only and has no `--apply-fix` option.
2. **Diagnosis is deterministic, not probabilistic.** It uses no LLM. When evidence confirms that a rollout is stuck but cannot distinguish between known causes, the finding uses an explicit `*-undetermined` cause and lists what was observed. It does not present the most likely cause as certain.
3. **It is scoped to one rollout**, not to a whole-cluster audit.

## Why it exists

Popeye, Polaris, kube-score, and k8sgpt already cover generic static analysis of manifests well. None of them watches one rollout and classifies its failure cause deterministically without depending on a language model. That gap is the reason for this project.

It is also why `check` prioritises verifications that need live namespace state—PodDisruptionBudget consistency, ResourceQuota headroom, requests versus real usage, whether the Services and Ingresses fronting the workload can actually route to it, and whether the ConfigMaps/Secrets it references actually exist—over checks that an offline linter already handles. Probe presence and limit presence remain included at low severity.

## Installation

### krew

The plugin is not in the krew index yet. This command does not work from the index today; it is the command that will work once the plugin is published there:

```bash
kubectl krew install safe-rollout
```

### Download a release binary

Download the appropriate binary from [GitHub Releases](https://github.com/HarnageaGabriel/kubectl-safe-rollout/releases). The installed binary must be named `kubectl-safe_rollout`—the underscore is krew's convention for multi-word plugins—and its directory must be on `PATH` so `kubectl` can discover it.

On Linux or macOS:

```bash
sudo install -m 0755 /path/to/downloaded-binary /usr/local/bin/kubectl-safe_rollout
kubectl plugin list
```

### From source

Go 1.26 or later is required.

```bash
go install github.com/HarnageaGabriel/kubectl-safe-rollout/cmd/kubectl-safe_rollout@latest
```

## Usage

```bash
kubectl safe-rollout check deployment/checkout
kubectl safe-rollout check deployment/checkout --output json
kubectl safe-rollout watch deployment/checkout
kubectl safe-rollout watch deployment/checkout --output json
kubectl safe-rollout watch deployment/checkout --timeout 10m
```

The plugin honours the current kubeconfig, context, and namespace, including `--context`, `--namespace`, and `--kubeconfig`. `watch` waits indefinitely by default, matching `kubectl rollout status`; pass `--timeout` to bound it, which reports a clear timeout message instead of hanging forever when a stall has no classifiable cause.

The exit code is non-zero if and only if the report contains at least one `high` severity finding. `medium` and `low` findings provide context and do not fail a CI pipeline.

## Example: check

This is real output from a kind cluster. The target is a three-replica Deployment covered by a PodDisruptionBudget with `minAvailable: 3`, with no probes or resource limits, in a cluster without metrics-server.

```text
HIGH  pdb-consistency          PodDisruptionBudget/checkout
      cause: PodDisruptionBudget "checkout" leaves no disruption headroom (minAvailable calculated over 3 replicas): a node drain concurrent with the rollout of Deployment/checkout will remain blocked until the pods are ready again
      evidence: minAvailable=3
      evidence: replicas=3
      evidence: disruptionsAllowed=0
      remediation: increase the PDB headroom (for example, maxUnavailable: 1 or minAvailable: 2) or increase the replicas of Deployment/checkout; the correct value depends on how many replicas can be lost in production
      note: remediation is context dependent, review before applying
OK    quota-headroom           no issues detected
SKIP  requests-vs-usage        pod metrics are not accessible (metrics-server absent or not ready yet): the server could not find the requested resource (get pods.metrics.k8s.io)
LOW   probe-sanity             Pod/checkout/app
      cause: container "app" in the pod template of workload "checkout" does not define a readinessProbe
      evidence: container=app
      remediation: add a readinessProbe to container "app" using a check that represents when the application can receive traffic
      note: remediation is context dependent, review before applying
LOW   probe-sanity             Pod/checkout/app
      cause: container "app" in the pod template of workload "checkout" does not define a livenessProbe
      evidence: container=app
      remediation: add a livenessProbe to container "app" using a check that detects when the application must be restarted
      note: remediation is context dependent, review before applying
LOW   resource-limits          Pod/checkout/app
      cause: container "app" in the pod template of workload "checkout" does not define a CPU limit
      evidence: container=app
      remediation: add a CPU limit to container "app" to make throttling predictable and isolate its consumption from other workloads on the node; the value depends on the application's profile
      note: remediation is context dependent, review before applying
LOW   resource-limits          Pod/checkout/app
      cause: container "app" in the pod template of workload "checkout" does not define a memory limit
      evidence: container=app
      remediation: add a memory limit to container "app" to contain consumption and prevent node-level OOMKills; the value depends on the application's profile
      note: remediation is context dependent, review before applying
LOW   image-pull-secrets       Pod/checkout/app
      cause: container "app" uses image "ghcr.io/stefanprodan/podinfo:6.7.0" from a registry that does not appear to be Docker Hub (inferred exclusively from the hostname, without checking the registry), but neither the Pod nor ServiceAccount "default" declares imagePullSecrets
      evidence: container=app
      evidence: image=ghcr.io/stefanprodan/podinfo:6.7.0
      evidence: serviceAccount=default
      remediation: if the registry for image "ghcr.io/stefanprodan/podinfo:6.7.0" requires authentication, add an imagePullSecret to the pod template or ServiceAccount "default"; if the registry is public on a custom domain, this finding is a false positive because the inference uses only the hostname
      note: remediation is context dependent, review before applying
```

The `SKIP` line is visible rather than silent: a check that could not evaluate must not look like a check that found nothing wrong. The `image-pull-secrets` finding also states that it infers registry behaviour from the hostname alone and may therefore be a false positive.

## Example: watch

This is real output from the same cluster against a Deployment whose container exits with code 1 at startup.

```text
HIGH  crashloop-app-error      Pod/payments-7484679db9-2x2h9
      cause: container "app" in Pod/payments-7484679db9-2x2h9 exits due to an application error (exit code 1), neither OOMKill nor liveness probe
      evidence: container=app
      evidence: restartCount=4
      evidence: exitCode=1 reason=Error
      evidence: log --previous (last lines): config file /etc/payments/config.yaml not found
      remediation: check the logs for container "app" to find the application cause of the exit
        $ kubectl logs payments-7484679db9-2x2h9 -c app -n demo-readme --previous
      note: remediation is context dependent, review before applying
OK    imagepull                no issues detected
OK    pending                  no issues detected
OK    quota                    no issues detected
OK    progress-deadline        no issues detected
```

The evidence includes the tail of the previous container's log. The suggested `kubectl logs --previous` command is read-only.

## What `check` verifies

| ID | Severity | What it needs from the cluster |
| --- | --- | --- |
| `pdb-consistency` | high/low | Workload replicas and update strategy; matching PodDisruptionBudget selectors, specification, and status. High for a PDB that leaves no disruption headroom; low (informational, not a verified blockage) when a workload with more than one replica has no matching PDB at all. |
| `quota-headroom` | high | Workload update strategy, calculated surge, pod resource requests, and live ResourceQuota hard and used values. |
| `hpa-quota-headroom` | medium | A HorizontalPodAutoscaler targeting this workload, its `maxReplicas`, and the same live ResourceQuota values as `quota-headroom`. An HPA can scale this workload up independently of any rollout; if that happens to coincide with a rollout window, the same quota wall blocks new pods, a trigger `quota-headroom`'s surge-only calculation cannot see. Medium, not high: an eroded margin, not a guaranteed blockage of the current rollout. |
| `serviceaccount-exists` | high | The ServiceAccount named in the pod template (or the implicit `default`). Not a heuristic: a Deployment naming one that does not exist will never create a single Pod. |
| `service-routing` | high | Services in the namespace whose selector matches the pod template's labels, their ports, and — once the workload has at least one ready pod — their EndpointSlices. Catches a rollout that succeeds by every other measure while the Service in front of it silently drops all traffic: a named `targetPort` no container declares, or zero ready endpoints despite ready pods. |
| `ingress-routing` | high | Ingresses in the namespace whose backends name a Service identified by `service-routing`, that Service's declared ports, and the Secret objects any such Ingress's `spec.tls` names. One hop further out than `service-routing`: an Ingress backend naming a port the Service does not declare, or a TLS Secret that does not exist, is a break the ingress controller hits even though the Service itself is perfectly healthy. |
| `config-references-exist` | high | Every ConfigMap and Secret the pod template references via `envFrom`, `env[].valueFrom`, or a volume source (one `get` per referenced name, never a namespace-wide `list`), and the specific key when a reference names one. The pre-flight counterpart to `watch`'s reactive `configerror-*`/`volumemount-*` classification. A reference marked `optional` is never reported when missing. Does not follow projected volume sources. |
| `pvc-exists` | high | Every PersistentVolumeClaim a pod template volume names by `claimName`. The pre-flight, unambiguous half of `watch`'s reactive `pending-unbound-pvc` (which also covers the more ambiguous "not yet Bound" case). Checks existence only, never binding status: a freshly created PVC is routinely Pending for a moment while its StorageClass provisions the volume, and that is not a problem. |
| `network-policy-ingress` | high | NetworkPolicies in the namespace whose `podSelector` matches the pod template's labels and whose `policyTypes` restrict ingress. Fires only when the union of `Ingress` rules across every such matching Policy is empty — a fact guaranteed by the NetworkPolicy API regardless of CNI. Correctly treats a deny-all baseline alongside an explicit allow Policy as healthy: NetworkPolicies are additive, and that combination is the standard default-deny idiom, not a break. |
| `requests-vs-usage` | medium | Selected live Pods and their requests plus the Pod Metrics API. It needs metrics-server and skips cleanly without it. |
| `probe-sanity` | low | Readiness and liveness probes in the live workload's pod template. |
| `resource-limits` | low | CPU and memory limits in the live workload's pod template, both regular and init containers. |
| `image-pull-secrets` | low/medium | Container image hostnames, and imagePullSecrets on the pod template, ServiceAccount, and the Secret objects they name. Low when nothing is declared (heuristic: the registry may still be public); medium when a declared Secret does not exist (a verified fact, not a guess). |

A PodDisruptionBudget is enforced by the Eviction API—for example, during `kubectl drain` or evictions initiated by the cluster-autoscaler or descheduler—and **not** by the Deployment controller when it replaces pods during a rolling update. `pdb-consistency` does not claim that a PDB blocks `kubectl rollout`; it reports that a node drain concurrent with the rollout window will stay blocked.

## What `watch` classifies

- Crash loop: `crashloop-oomkilled`, `crashloop-liveness-probe`, `crashloop-app-error`.
- Container configuration: `configerror-missing-configmap`, `configerror-missing-secret`, `configerror-undetermined`.
- Image pull: `imagepull-invalid-reference`, `imagepull-tag-not-found`, `imagepull-registry-unreachable`, `imagepull-credentials-missing`.
- Volume mount: `volumemount-missing-secret`, `volumemount-missing-configmap`, `volumemount-undetermined`.
- Init container: `initcontainer-app-error`, `initcontainer-oomkilled`, `initcontainer-undetermined`.
- Readiness: `readiness-probe-failing`, `readiness-undetermined`. Readiness is reported only after the Deployment controller has concluded that the rollout is not progressing, so a slow-starting application is not flagged.
- Pending: `pending-insufficient-resources`, `pending-scheduling-constraints`, `pending-unbound-pvc`.
- Pod creation rejected: `quota-exceeded`, `serviceaccount-missing` (the pod template references a ServiceAccount that does not exist), `quota-undetermined`.
- Progress deadline: `progress-deadline-exceeded`.
- Paused rollout: `rollout-paused`, reported immediately from `spec.paused` rather than waiting for a symptom that will never appear. Because it fires with no pod symptom at all, `watch` spends a short settle window (longer if a container has already restarted) checking for another, coexisting cause before finalizing — a rollout paused after it started crash-looping reports both, not just that it is paused.
- StatefulSet update stuck (StatefulSet only): `statefulset-update-ondelete` (the `OnDelete` strategy has a pending update — `status.updateRevision` differs from `status.currentRevision` — but the controller never creates, updates, or deletes a Pod on its own until the operator deletes them one at a time) and `statefulset-partition-blocked` (a `RollingUpdate` `partition` at or above the replica count makes every pod ordinal ineligible for the pending update). Neither fires for the standard `0 < partition < replicas` canary idiom, which is deliberate, healthy throttling, not a stuck rollout.

Each category has an `*-undetermined` variant for confirmed failures that lack enough evidence for a more specific cause. These IDs are stable: JSON output exposes them, and CI gates should key on them.

## Non-goals

- No dashboard or TUI.
- No SaaS.
- No cost reporting.
- No LLM-based diagnosis.
- No cluster mutation.
- Not a replacement for Argo Rollouts or Flagger.

## Verified and not verified

Verified: 20 end-to-end scenarios on kind v0.32.0 with containerd 2.3.1, all passing against v1.36.1. 19 of them, including the `rollout-paused` scenario, have additionally been run in full against **three Kubernetes minor versions** — v1.36.1, v1.35.5 and v1.34.8 — and pass on each; the 20th (`serviceaccount-missing`) is newly added and, as of this writing, has not yet been re-verified across all three.

Of those scenarios, 16 cover the classified causes, one is a slow-start regression that guards against readiness false positives by requiring a completed rollout with no finding at all, and one runs `check` as a deliberately restricted ServiceAccount to prove that a check which cannot read a resource degrades to a visible `SKIP` rather than failing the run or reporting a clean result.

They run with `make test-e2e` against a real cluster, against real kubelet, scheduler and containerd event messages rather than fixtures. `make test-e2e-versions` repeats the suite across the three minors.

Not verified:

- CRI-O. Only containerd has been exercised, so the event-message patterns for other runtimes are unconfirmed. This is the largest remaining gap: the messages this tool matches are produced by the runtime, not by Kubernetes.
- Reconnection after a real etcd compaction or HTTP 410 `resourceVersion` expiry. That path has only been exercised against a fake clientset.
- API load and event correlation on large, busy namespaces.
- StatefulSet, end to end. `check` and `watch` accept StatefulSet, and `internal/workload`/`internal/diagnose` have unit tests (fake clientset) for it, but the e2e scenarios in `test/e2e/statefulset_test.go` have not been run against a real cluster on the machine that wrote them (no Docker/kind available there). Deployment remains the only kind verified end to end so far.

This list exists because a diagnosis tool that overstates what it has tested is worse than one that reports less.

## Development

```bash
make build
make test
make lint
make cover
```

The end-to-end suite requires Docker:

```bash
make kind-up
make test-e2e
make kind-down
```

Read [CONTRIBUTING.md](CONTRIBUTING.md), the [security policy](SECURITY.md), and the [code of conduct](CODE_OF_CONDUCT.md) before contributing. Project conventions are in [rules/](rules/), and the Watch API design decision is documented in [docs/watch-vs-polling.md](docs/watch-vs-polling.md).

## License

Licensed under the [Apache License 2.0](LICENSE). There is no NOTICE file because the repository vendors no third-party sources; see [rules/license.md](rules/license.md) for the rationale.
