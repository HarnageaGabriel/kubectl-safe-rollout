// Copyright 2026 Gabriel Harnagea
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package diagnose

// CauseID is the stable identifier for a classified cause of rollout
// failure or stall. It is the value that every Diagnoser writes to
// model.Finding.CheckID (not "crashloop", the category name: the specific
// cause, e.g. "crashloop-oomkilled"), so consumers of the JSON output can
// gate in CI on a single cause rather than the entire category.
//
// Every category whose observed signal can have multiple causes has its own
// "-undetermined" variant: it is the cause a Diagnoser reports when the
// available evidence confirms that the rollout is blocked but is not
// enough to distinguish among the category's known causes. There is no
// generic CauseID value for "undetermined without context": the project's
// determinism constraint requires even an undetermined cause to declare at
// least the category where it was observed, never an empty label.
type CauseID string

const (
	// CauseCrashLoopOOMKilled means the kernel kills the container for
	// exceeding its memory limit. Structured signal
	// (ContainerStateTerminated.Reason == "OOMKilled"), no free-text
	// parsing required.
	CauseCrashLoopOOMKilled CauseID = "crashloop-oomkilled"
	// CauseCrashLoopLivenessProbe means the kubelet terminates the
	// container because the liveness probe repeatedly fails, not because
	// the app crashes on its own. Signal: event Reason=="Killing" with a
	// message mentioning the liveness probe.
	CauseCrashLoopLivenessProbe CauseID = "crashloop-liveness-probe"
	// CauseCrashLoopAppError means the container exits with an application
	// exit code, neither OOMKilled nor killed by the probe.
	CauseCrashLoopAppError CauseID = "crashloop-app-error"
	// CauseCrashLoopUndetermined means the container is in
	// CrashLoopBackOff but the available evidence (missing
	// LastTerminationState or empty Reason, no Killing event for liveness)
	// does not allow choosing among the three causes above.
	CauseCrashLoopUndetermined CauseID = "crashloop-undetermined"

	// CauseConfigErrorMissingConfigMap means the kubelet cannot build the
	// container configuration because its Waiting.Message names a missing
	// ConfigMap while Waiting.Reason is CreateContainerConfigError.
	CauseConfigErrorMissingConfigMap CauseID = "configerror-missing-configmap"
	// CauseConfigErrorMissingSecret means the kubelet cannot build the
	// container configuration because its Waiting.Message names a missing
	// Secret while Waiting.Reason is CreateContainerConfigError.
	CauseConfigErrorMissingSecret CauseID = "configerror-missing-secret"
	// CauseConfigErrorUndetermined means Waiting.Reason confirms a
	// CreateContainerConfigError but Waiting.Message names no recognized
	// missing ConfigMap or Secret.
	CauseConfigErrorUndetermined CauseID = "configerror-undetermined"

	// CauseImagePullInvalidReference means Waiting.Reason is
	// InvalidImageName: the image reference is malformed and no registry was
	// contacted.
	CauseImagePullInvalidReference CauseID = "imagepull-invalid-reference"
	// CauseImagePullTagNotFound means the requested tag or digest does not
	// exist in the registry.
	CauseImagePullTagNotFound CauseID = "imagepull-tag-not-found"
	// CauseImagePullRegistryUnreachable means the registry does not respond
	// (DNS, network, timeout); this is not an authorization or tag issue.
	CauseImagePullRegistryUnreachable CauseID = "imagepull-registry-unreachable"
	// CauseImagePullUnauthorized means the pull is rejected because
	// authentication/authorization is missing or insufficient
	// (imagePullSecrets is missing or expired).
	CauseImagePullUnauthorized CauseID = "imagepull-credentials-missing"
	// CauseImagePullUndetermined means the container is in
	// ImagePullBackOff/ErrImagePull but the Failed event message matches no
	// known pattern (unexpected runtime or message format).
	CauseImagePullUndetermined CauseID = "imagepull-undetermined"

	// CauseVolumeMountMissingSecret means a FailedMount event names a Secret
	// that does not exist in the Pod namespace.
	CauseVolumeMountMissingSecret CauseID = "volumemount-missing-secret"
	// CauseVolumeMountMissingConfigMap means a FailedMount event names a
	// ConfigMap that does not exist in the Pod namespace.
	CauseVolumeMountMissingConfigMap CauseID = "volumemount-missing-configmap"
	// CauseVolumeMountUndetermined means a FailedMount event confirms the
	// volume setup failure but its message names no recognized missing Secret
	// or ConfigMap.
	CauseVolumeMountUndetermined CauseID = "volumemount-undetermined"

	// CauseInitContainerAppError means an init container repeatedly exits
	// non-zero for a reason other than OOMKilled. Structured signal: a
	// non-zero Terminated.ExitCode after at least one restart.
	CauseInitContainerAppError CauseID = "initcontainer-app-error"
	// CauseInitContainerOOMKilled means an init container is terminated with
	// structured Reason OOMKilled after exceeding its memory limit.
	CauseInitContainerOOMKilled CauseID = "initcontainer-oomkilled"
	// CauseInitContainerUndetermined means an init container is in
	// CrashLoopBackOff but its termination state does not identify OOMKilled
	// or a non-zero application exit.
	CauseInitContainerUndetermined CauseID = "initcontainer-undetermined"

	// CauseReadinessProbeFailing means a container remains running but not
	// ready after the rollout exceeds its progress deadline, with an
	// Unhealthy event confirming repeated readiness probe failures.
	CauseReadinessProbeFailing CauseID = "readiness-probe-failing"
	// CauseReadinessUndetermined means a container remains running but not
	// ready after the rollout exceeds its progress deadline, but no
	// Unhealthy readiness-probe event was observed to explain why.
	CauseReadinessUndetermined CauseID = "readiness-undetermined"

	// CausePendingInsufficientResources means no node has enough CPU,
	// memory, or ephemeral storage for scheduling.
	CausePendingInsufficientResources CauseID = "pending-insufficient-resources"
	// CausePendingSchedulingConstraints means nodeSelector, affinity, or
	// taint/toleration prevents scheduling on every available node.
	CausePendingSchedulingConstraints CauseID = "pending-scheduling-constraints"
	// CausePendingUnboundPVC means the pod references a
	// PersistentVolumeClaim that is not yet Bound (or does not exist).
	// Structured signal: PersistentVolumeClaim.Status.Phase, not a text
	// pattern.
	CausePendingUnboundPVC CauseID = "pending-unbound-pvc"
	// CausePendingUndetermined means the pod is Pending but has no
	// recognizable FailedScheduling event or referenced unbound PVC.
	CausePendingUndetermined CauseID = "pending-undetermined"

	// CauseQuotaExceeded means the object that owns pod creation (a
	// ReplicaSet for Deployment, the StatefulSet itself for StatefulSet;
	// see PodCreationSource) cannot create pods because the ResourceQuota
	// admission plugin rejects the request. Not observable on Pods (they
	// never exist): observable only as a Reason=="FailedCreate" event on
	// that object.
	CauseQuotaExceeded CauseID = "quota-exceeded"
	// CauseQuotaUndetermined means the object that owns pod creation (a
	// ReplicaSet for Deployment, the StatefulSet itself for StatefulSet)
	// has a FailedCreate event but the message does not mention an
	// exceeded quota (it could be an admission webhook or another
	// rejection): the failure to create pods is still a real signal to
	// report, but the specific cause is not.
	CauseQuotaUndetermined CauseID = "quota-undetermined"

	// CauseServiceAccountMissing means the object that owns pod creation (a
	// ReplicaSet for Deployment, the StatefulSet itself for StatefulSet)
	// cannot create pods because the pod template names a ServiceAccount
	// that does not exist in the namespace. Same signal source as the
	// quota causes above (a Reason=="FailedCreate" event on that object,
	// the pod never exists), but a distinct and fully deterministic cause:
	// found on kind via a Deployment referencing a ServiceAccount that had
	// never been created, which the ResourceQuota diagnoser was
	// mislabeling as "quota-undetermined" before this cause existed.
	CauseServiceAccountMissing CauseID = "serviceaccount-missing"

	// CauseProgressDeadlineExceeded means the Deployment controller has
	// already concluded on its own that the rollout is not progressing
	// within spec.progressDeadlineSeconds. There is no ambiguity to resolve
	// here: the condition itself is the cause, read from Status.Conditions,
	// not derived from a text pattern.
	CauseProgressDeadlineExceeded CauseID = "progress-deadline-exceeded"

	// CauseRolloutPaused means spec.paused is true: the Deployment
	// controller takes no action on a paused rollout at all, including
	// never evaluating progressDeadlineSeconds against it (Kubernetes
	// freezes that deadline while paused). Watch would otherwise wait
	// forever without ever producing a Finding. Like
	// CauseProgressDeadlineExceeded, there is no ambiguity to resolve: the
	// boolean field itself is the cause, not a text pattern to interpret.
	CauseRolloutPaused CauseID = "rollout-paused"

	// CauseStatefulSetUpdateOnDelete means a StatefulSet uses the OnDelete
	// update strategy and has a pending update (Status.UpdateRevision
	// differs from Status.CurrentRevision). The controller sets
	// UpdateRevision as soon as the pod template changes, but under
	// OnDelete it never creates, updates, or deletes a Pod on its own:
	// nothing happens until the operator deletes the pods that must move to
	// the new revision, one at a time. Unlike CauseRolloutPaused, this is
	// not a controller-wide freeze triggered by a boolean the operator set
	// deliberately for this rollout; it is the standing, documented
	// behavior of a strategy an operator may have chosen for entirely
	// unrelated reasons (e.g. to control the exact timing of each pod's
	// replacement themselves).
	CauseStatefulSetUpdateOnDelete CauseID = "statefulset-update-ondelete"
	// CauseStatefulSetPartitionBlocked means a StatefulSet's RollingUpdate
	// strategy has spec.updateStrategy.rollingUpdate.partition set to a
	// value greater than or equal to the desired replica count while a
	// update is pending (Status.UpdateRevision differs from
	// Status.CurrentRevision): the controller only updates pods whose
	// ordinal is greater than or equal to partition, so a partition at or
	// above the replica count makes every pod ineligible and the pending
	// revision mathematically unreachable. 0 < partition < replicas is the
	// standard canary idiom (only the highest-ordinal pods update) and is
	// deliberately excluded: that is healthy, intentional throttling, not a
	// stuck rollout.
	CauseStatefulSetPartitionBlocked CauseID = "statefulset-partition-blocked"
)
