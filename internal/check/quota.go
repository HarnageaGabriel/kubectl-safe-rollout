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

package check

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// QuotaHeadroomCheckID is the stable identifier for this check.
const QuotaHeadroomCheckID = "quota-headroom"

// QuotaHeadroom checks that namespace ResourceQuotas leave enough headroom
// for rolling update surge: number of pods ("pods" key) and CPU/memory
// requested by additional pods ("cpu"/"requests.cpu" and
// "memory"/"requests.memory" keys). If the headroom is insufficient, the
// surge cannot start and the rollout remains blocked with Pending pods,
// long before `watch` gets a chance to diagnose it.
//
// Deliberate simplifications consistent with the MVP: a ResourceQuota with
// a scopeSelector (for example, limited to BestEffort pods) is treated as
// though it still applied to all pods in the namespace—an occasional false
// positive costs less than silently overestimating headroom. PodRequests
// sums only regular containers, not init containers (see
// workload.Workload.PodRequests).
type QuotaHeadroom struct{}

// ID implements check.Check.
func (QuotaHeadroom) ID() string { return QuotaHeadroomCheckID }

// Run implements check.Check.
func (c QuotaHeadroom) Run(ctx context.Context, target Target) (Result, error) {
	quotaList, err := target.Client.CoreV1().ResourceQuotas(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("ResourceQuota list is not accessible: %v", err)), nil
	}
	if len(quotaList.Items) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	surge, ok := surgeCount(target.Workload)
	if !ok {
		// Recreate, or maxSurge=0: no additional pods to cover.
		return Result{CheckID: c.ID()}, nil
	}

	perPod := target.Workload.PodRequests()
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	var findings []model.Finding
	for _, q := range quotaList.Items {
		resourceRef := model.ResourceRef{Kind: "ResourceQuota", Namespace: q.Namespace, Name: q.Name}

		if f, ok := podCountFinding(q, resourceRef, workloadRef, surge); ok {
			findings = append(findings, f)
		}
		for _, resName := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if f, ok := computeFinding(q, resourceRef, workloadRef, surge, resName, perPod[resName]); ok {
				findings = append(findings, f)
			}
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// surgeCount calculates how many pods above the desired replica count the
// rolling update can create at once. ok=false when the strategy does not
// surge (Recreate) or the calculation yields zero: in either case there is
// no additional headroom to check.
func surgeCount(w workload.Workload) (int32, bool) {
	strategy := w.UpdateStrategy()
	if strategy.Type != workload.RollingUpdate || strategy.MaxSurge == nil {
		return 0, false
	}
	v, err := intstr.GetScaledValueFromIntOrPercent(strategy.MaxSurge, int(w.Replicas()), true)
	if err != nil || v <= 0 {
		return 0, false
	}
	return int32(v), true
}

func podCountFinding(q corev1.ResourceQuota, resourceRef model.ResourceRef, workloadRef string, surge int32) (model.Finding, bool) {
	hard, used, ok := headroom(q, corev1.ResourcePods, "")
	if !ok {
		return model.Finding{}, false
	}
	available := hard.Value() - used.Value()
	if available >= int64(surge) {
		return model.Finding{}, false
	}
	return model.Finding{
		CheckID:  QuotaHeadroomCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"the rolling update of %s can create up to %d additional pods during surge, but ResourceQuota %q leaves headroom for only %d additional pods (hard=%s, used=%s)",
			workloadRef, surge, q.Name, available, hard.String(), used.String(),
		),
		Evidence: []string{
			fmt.Sprintf("surge=%d", surge),
			fmt.Sprintf("quota pods hard=%s used=%s", hard.String(), used.String()),
		},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("increase the \"pods\" quota of %q, reduce maxSurge for %s, or free capacity by terminating other workloads; the correct choice depends on who shares the namespace", q.Name, workloadRef),
			ContextDependent: true,
		},
		Resource: resourceRef,
	}, true
}

func computeFinding(q corev1.ResourceQuota, resourceRef model.ResourceRef, workloadRef string, surge int32, resName corev1.ResourceName, perPod resource.Quantity) (model.Finding, bool) {
	hard, used, ok := headroom(q, resName, "requests.")
	if !ok || perPod.IsZero() {
		return model.Finding{}, false
	}
	available := hard.DeepCopy()
	available.Sub(used)
	needed := *resource.NewMilliQuantity(perPod.MilliValue()*int64(surge), perPod.Format)
	if available.Cmp(needed) >= 0 {
		return model.Finding{}, false
	}
	return model.Finding{
		CheckID:  QuotaHeadroomCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"the rolling update of %s requires up to %s of additional %s during surge (%d pods), but ResourceQuota %q leaves only %s of headroom",
			workloadRef, needed.String(), resName, surge, q.Name, available.String(),
		),
		Evidence: []string{
			fmt.Sprintf("surge=%d perPodRequest.%s=%s", surge, resName, perPod.String()),
			fmt.Sprintf("quota %s hard=%s used=%s", resName, hard.String(), used.String()),
		},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("increase the %s quota on %q, reduce maxSurge for %s, or reduce container requests; the correct choice depends on how much extra capacity the namespace can provide during a rollout", resName, q.Name, workloadRef),
			ContextDependent: true,
		},
		Resource: resourceRef,
	}, true
}

// headroom reads a ResourceQuota's hard/used pair for a given resource,
// trying the prefixed key first (for example, "requests.cpu") and then the
// unprefixed alias (for example, "cpu", equivalent for quotas): not all
// ResourceQuotas use the same key style. ok=false if the quota does not
// constrain this resource at all.
func headroom(q corev1.ResourceQuota, name corev1.ResourceName, prefix string) (hard, used resource.Quantity, ok bool) {
	keys := []corev1.ResourceName{name}
	if prefix != "" {
		keys = []corev1.ResourceName{corev1.ResourceName(prefix + string(name)), name}
	}
	for _, k := range keys {
		if h, exists := q.Status.Hard[k]; exists {
			u := q.Status.Used[k]
			return h, u, true
		}
	}
	return resource.Quantity{}, resource.Quantity{}, false
}
