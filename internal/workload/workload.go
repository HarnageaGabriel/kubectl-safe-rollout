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

// Package workload abstracts differences among Kubernetes workload types
// (Deployment and StatefulSet; DaemonSet remains unsupported) behind a
// single interface. Checks in internal/check operate on Workload, not
// concrete types from k8s.io/api/apps/v1: this prevents every new check from
// having to handle a switch on Deployment/StatefulSet/DaemonSet.
package workload

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// UpdateStrategy normalizes the update strategies of different controllers
// (RollingUpdate/Recreate for Deployment, RollingUpdate/OnDelete for
// StatefulSet) into a common form. MaxUnavailable and MaxSurge are nil when
// the controller does not support them (e.g. Recreate), not when they are
// zero: the distinction matters to checks.
type UpdateStrategy struct {
	Type           string
	MaxUnavailable *intstr.IntOrString
	MaxSurge       *intstr.IntOrString
	// Partition is meaningful only for StatefulSet's RollingUpdate strategy:
	// the ordinal at which the update is partitioned (pods with an ordinal
	// below Partition are left untouched by the rollout). nil for Deployment,
	// and nil for a StatefulSet that does not set it explicitly — kept
	// distinguishable from an explicit 0, the same convention already used
	// for MaxUnavailable/MaxSurge.
	Partition *int32
}

// Recreate and RollingUpdate replicate the appsv1 constants to avoid forcing
// callers to import k8s.io/api/apps/v1 only to compare the strategy type.
// OnDelete has no Deployment equivalent: it exists only for StatefulSet.
const (
	Recreate        = "Recreate"
	RollingUpdate   = "RollingUpdate"
	OnDelete        = "OnDelete"
	defaultMaxUnav  = "25%"
	defaultMaxSurge = "25%"
)

// Workload is the view of a live Deployment/StatefulSet/... exposed to
// pre-flight checks and watch diagnosis.
type Workload interface {
	Kind() string
	Name() string
	Namespace() string
	// UID identifies the live object: internal/diagnose uses it to filter
	// ReplicaSets owned by the workload (OwnerReferences); checks in
	// internal/check do not need it.
	UID() types.UID
	// Replicas is the desired replica count, never nil: a Deployment with
	// Spec.Replicas unset defaults to 1 in Kubernetes, and this method applies
	// that default so callers do not have to remember it.
	Replicas() int32
	// PodLabels are the pod template labels, used to match against a
	// PodDisruptionBudget selector and to observe pods in `watch`.
	PodLabels() map[string]string
	// PodSelector is the controller's immutable selector, not all labels from
	// the current template. It also includes pods and ReplicaSets from previous
	// revisions during a rollout.
	PodSelector() (labels.Selector, error)
	UpdateStrategy() UpdateStrategy
	// PodRequests sums requests (not limits) for all regular containers in the
	// pod template, to compare them with ResourceQuota headroom during a surge.
	// It sums only regular containers: init containers have different effective
	// request semantics (the maximum of the largest init-container request and
	// the sum of regular-container requests, not an additional addend), a detail
	// the quota-headroom check alone does not need to model to preserve a
	// reasonable margin.
	PodRequests() corev1.ResourceList
	// PodContainers exposes complete regular ContainerSpecs for checks that
	// must read per-container Probe and Resources: a dedicated DTO would add
	// indirection without hiding Kubernetes details.
	PodContainers() []corev1.Container
	// InitContainers exposes the pod template's init containers, kept
	// separate from PodContainers because the two are not interchangeable
	// for every check: probes on init containers are not a real Kubernetes
	// feature (kubelet does not act on them, since an init container runs
	// to completion rather than continuously), so probe-sanity has no
	// reason to read this. Resource limits, on the other hand, apply to an
	// init container exactly as they do to a regular one — the kubelet
	// enforces its cgroup limits the same way, and an init container
	// without a memory limit can OOM the node just as a regular one can.
	InitContainers() []corev1.Container
	// Volumes exposes the pod template's volumes, for checks that must
	// verify a ConfigMap or Secret a volume source names actually exists:
	// a volume can reference either independently of any container's env
	// or envFrom.
	Volumes() []corev1.Volume
	// ImagePullSecretNames exposes the names of secrets declared directly in
	// the pod template. Secrets inherited from the ServiceAccount require a
	// cluster read and remain the responsibility of the check that needs them.
	ImagePullSecretNames() []string
	// ServiceAccountName exposes the name declared in the pod template. An
	// empty string indicates the Kubernetes "default" ServiceAccount: the
	// getter does not apply the default, keeping that decision visible to the
	// caller.
	ServiceAccountName() string
	// PriorityClassName exposes spec.priorityClassName from the pod template.
	// It is a single field on PodSpec, not per-container, unlike resource
	// limits: there is exactly one name to resolve per workload. An empty
	// string means unset (the pod priority defaults to the cluster default,
	// or zero if there is none) and is a legitimate, common configuration,
	// not something the caller should treat as "nothing to verify" the way
	// ServiceAccountName's empty string still resolves to "default" — the two
	// getters deliberately do not share that convention, see the doc comment
	// on PodSpec.PriorityClassName in k8s.io/api/core/v1/types.go.
	PriorityClassName() string
	// TopologySpreadConstraints exposes spec.topologySpreadConstraints from
	// the pod template, used by scheduling-constraints-feasibility to
	// evaluate whether the constraint can actually be satisfied by the
	// cluster's current node topology.
	TopologySpreadConstraints() []corev1.TopologySpreadConstraint
	// Affinity exposes spec.affinity from the pod template as-is (may be
	// nil). scheduling-constraints-feasibility reads NodeAffinity (to
	// narrow candidate nodes) and PodAntiAffinity (to evaluate required
	// self-anti-affinity) from it; no other check needs it today, so this
	// stays a single passthrough rather than several narrower accessors.
	Affinity() *corev1.Affinity
	// NodeSelector exposes spec.nodeSelector from the pod template. Kept
	// separate from Affinity because it is a distinct PodSpec field that
	// combines with node affinity (both must match), not a part of it.
	NodeSelector() map[string]string
	// Tolerations exposes spec.tolerations from the pod template, used to
	// determine which nodes a new pod of this workload can actually land
	// on when a candidate node is tainted.
	Tolerations() []corev1.Toleration
	// SchedulerName exposes spec.schedulerName from the pod template. An
	// empty string means the Kubernetes default ("default-scheduler") is
	// implied but not written explicitly; callers that need to compare
	// against the default must apply that default themselves, the same
	// convention already used by ServiceAccountName.
	SchedulerName() string
	// RolloutComplete reports whether the rollout completed successfully:
	// replicas are updated and available, and the controller observed the
	// current Spec generation. Used by `watch` to stop observation on the
	// successful path.
	RolloutComplete() bool
	// ProgressDeadlineExceeded reports whether the controller has already
	// concluded on its own that the rollout is not progressing within its
	// deadline. ok=false means "not applicable to this controller type or not
	// yet exceeded", never "definitely not exceeded": callers must not confuse
	// the two.
	ProgressDeadlineExceeded() (message string, ok bool)
	// Paused reports spec.paused. A paused Deployment's controller takes no
	// action at all: it does not create or update Pods, and Kubernetes
	// itself freezes progressDeadlineSeconds while paused, so
	// ProgressDeadlineExceeded never fires either. Without this, `watch`
	// would wait indefinitely on a paused rollout with no way to explain
	// why.
	Paused() bool
	// PendingRevisionUpdate reports the controller's desired and current
	// revision hashes, and whether this concept applies to this controller
	// type at all. It exists for StatefulSet's OnDelete strategy: the
	// controller sets status.updateRevision as soon as the pod template
	// changes but, under OnDelete, never acts on a Pod itself (see
	// UpdateStrategy's OnDelete constant) — updateRevision != currentRevision
	// is the only available signal that an update is pending, since nothing
	// else in Status changes. ok=false means "not applicable to this
	// controller type" (always the case for Deployment, whose Status carries
	// no revision-hash pair), never "definitely no pending update" —
	// matching the convention already established by
	// ProgressDeadlineExceeded/Paused.
	PendingRevisionUpdate() (updateRevision, currentRevision string, ok bool)
}

type deploymentWorkload struct {
	d *appsv1.Deployment
}

// FromDeployment builds a Workload from a live Deployment.
func FromDeployment(d *appsv1.Deployment) Workload {
	return &deploymentWorkload{d: d}
}

func (w *deploymentWorkload) Kind() string      { return "Deployment" }
func (w *deploymentWorkload) Name() string      { return w.d.Name }
func (w *deploymentWorkload) Namespace() string { return w.d.Namespace }
func (w *deploymentWorkload) UID() types.UID    { return w.d.UID }

func (w *deploymentWorkload) Replicas() int32 {
	if w.d.Spec.Replicas == nil {
		return 1
	}
	return *w.d.Spec.Replicas
}

func (w *deploymentWorkload) PodLabels() map[string]string {
	return w.d.Spec.Template.Labels
}

func (w *deploymentWorkload) PodSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.d.Spec.Selector)
}

func (w *deploymentWorkload) UpdateStrategy() UpdateStrategy {
	strategyType := w.d.Spec.Strategy.Type
	if strategyType == "" {
		strategyType = RollingUpdate
	}
	if strategyType == appsv1.RecreateDeploymentStrategyType {
		return UpdateStrategy{Type: Recreate}
	}

	ru := w.d.Spec.Strategy.RollingUpdate
	maxUnavailable := intstr.FromString(defaultMaxUnav)
	maxSurge := intstr.FromString(defaultMaxSurge)
	if ru != nil {
		if ru.MaxUnavailable != nil {
			maxUnavailable = *ru.MaxUnavailable
		}
		if ru.MaxSurge != nil {
			maxSurge = *ru.MaxSurge
		}
	}
	return UpdateStrategy{
		Type:           RollingUpdate,
		MaxUnavailable: &maxUnavailable,
		MaxSurge:       &maxSurge,
	}
}

// PodRequests implements Workload.
func (w *deploymentWorkload) PodRequests() corev1.ResourceList {
	total := corev1.ResourceList{}
	for _, c := range w.d.Spec.Template.Spec.Containers {
		for name, qty := range c.Resources.Requests {
			sum := total[name]
			sum.Add(qty)
			total[name] = sum
		}
	}
	return total
}

// PodContainers implements Workload.
func (w *deploymentWorkload) PodContainers() []corev1.Container {
	return w.d.Spec.Template.Spec.Containers
}

// InitContainers implements Workload.
func (w *deploymentWorkload) InitContainers() []corev1.Container {
	return w.d.Spec.Template.Spec.InitContainers
}

// Volumes implements Workload.
func (w *deploymentWorkload) Volumes() []corev1.Volume {
	return w.d.Spec.Template.Spec.Volumes
}

// ImagePullSecretNames implements Workload.
func (w *deploymentWorkload) ImagePullSecretNames() []string {
	refs := w.d.Spec.Template.Spec.ImagePullSecrets
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}

// ServiceAccountName implements Workload.
func (w *deploymentWorkload) ServiceAccountName() string {
	return w.d.Spec.Template.Spec.ServiceAccountName
}

// PriorityClassName implements Workload.
func (w *deploymentWorkload) PriorityClassName() string {
	return w.d.Spec.Template.Spec.PriorityClassName
}

// TopologySpreadConstraints implements Workload.
func (w *deploymentWorkload) TopologySpreadConstraints() []corev1.TopologySpreadConstraint {
	return w.d.Spec.Template.Spec.TopologySpreadConstraints
}

// Affinity implements Workload.
func (w *deploymentWorkload) Affinity() *corev1.Affinity {
	return w.d.Spec.Template.Spec.Affinity
}

// NodeSelector implements Workload.
func (w *deploymentWorkload) NodeSelector() map[string]string {
	return w.d.Spec.Template.Spec.NodeSelector
}

// Tolerations implements Workload.
func (w *deploymentWorkload) Tolerations() []corev1.Toleration {
	return w.d.Spec.Template.Spec.Tolerations
}

// SchedulerName implements Workload.
func (w *deploymentWorkload) SchedulerName() string {
	return w.d.Spec.Template.Spec.SchedulerName
}

// RolloutComplete replicates the `kubectl rollout status` logic for
// Deployment (pkg/polymorphichelpers/rollout_status.go): the current Spec
// must have been observed by the controller, updated replicas must have
// reached the desired count, no old replicas may remain pending termination,
// and all updated replicas must be available. Replicating the same calculation
// as a tool the ecosystem already considers correct avoids reinventing a
// slightly different definition of "success" that is difficult to justify.
func (w *deploymentWorkload) RolloutComplete() bool {
	d := w.d
	if d.Generation > d.Status.ObservedGeneration {
		return false
	}
	desired := w.Replicas()
	if d.Status.UpdatedReplicas < desired {
		return false
	}
	if d.Status.Replicas > d.Status.UpdatedReplicas {
		return false
	}
	if d.Status.AvailableReplicas < d.Status.UpdatedReplicas {
		return false
	}
	return true
}

// progressDeadlineExceededReason is the Reason value written by the
// Deployment controller to the "Progressing" condition when it exceeds
// spec.progressDeadlineSeconds (deploymentutil.TimedOutReason in
// k8s.io/kubernetes, which cannot be imported here: it is a stable literal,
// part of the controller's observable contract, not a log message subject to
// rewording).
const progressDeadlineExceededReason = "ProgressDeadlineExceeded"

// ProgressDeadlineExceeded reads the "Progressing" condition from the
// Deployment Status: the controller itself has already concluded that the
// rollout is stuck; this project does not infer it from its own timeout.
func (w *deploymentWorkload) ProgressDeadlineExceeded() (message string, ok bool) {
	for _, c := range w.d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Reason == progressDeadlineExceededReason {
			return c.Message, true
		}
	}
	return "", false
}

// Paused implements Workload.
func (w *deploymentWorkload) Paused() bool {
	return w.d.Spec.Paused
}

// PendingRevisionUpdate implements Workload. Deployment's Status carries no
// revision-hash pair comparable to StatefulSet's UpdateRevision/
// CurrentRevision, so this is never applicable.
func (w *deploymentWorkload) PendingRevisionUpdate() (updateRevision, currentRevision string, ok bool) {
	return "", "", false
}
