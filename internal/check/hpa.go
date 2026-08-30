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

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// HPAQuotaHeadroomCheckID is the stable identifier for this check.
const HPAQuotaHeadroomCheckID = "hpa-quota-headroom"

// HPAQuotaHeadroom checks whether ResourceQuota has enough headroom for a
// HorizontalPodAutoscaler targeting this workload to actually reach its
// configured maxReplicas. quota-headroom already checks headroom for
// rolling-update surge; this is a distinct, independent trigger that
// quota-headroom's surge-only calculation cannot see: an HPA can scale
// this workload up on its own schedule, driven by load, with no relation
// to any rollout in progress. If that scale-up happens to land during a
// rollout window — a realistic production scenario, since a traffic
// spike is a plausible reason for both a deploy and a scale event to
// coincide — the same quota wall blocks new pods, and quota-headroom's
// surge number never warned about it because it was never the limiting
// factor it modeled.
//
// Severity Medium, not High like quota-headroom: this is not a guaranteed
// blockage of the current rollout attempt, only a margin that erodes if
// the HPA happens to fire while the quota is already tight — the same
// distinction already applied to requests-vs-usage.
type HPAQuotaHeadroom struct{}

// ID implements check.Check.
func (HPAQuotaHeadroom) ID() string { return HPAQuotaHeadroomCheckID }

// Run implements check.Check.
func (c HPAQuotaHeadroom) Run(ctx context.Context, target Target) (Result, error) {
	hpaList, err := target.Client.AutoscalingV2().HorizontalPodAutoscalers(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("HorizontalPodAutoscaler list is not accessible: %v", err)), nil
	}

	var targeting *autoscalingv2.HorizontalPodAutoscaler
	for i := range hpaList.Items {
		hpa := &hpaList.Items[i]
		ref := hpa.Spec.ScaleTargetRef
		if ref.Kind == target.Workload.Kind() && ref.Name == target.Workload.Name() {
			targeting = hpa
			break
		}
	}
	if targeting == nil {
		return Result{CheckID: c.ID()}, nil
	}

	additional := int64(targeting.Spec.MaxReplicas) - int64(target.Workload.Replicas())
	if additional <= 0 {
		// maxReplicas is already at or below the current desired replica
		// count: the HPA cannot ask for anything beyond what already
		// exists, so there is no extra headroom to verify.
		return Result{CheckID: c.ID()}, nil
	}

	quotaList, err := target.Client.CoreV1().ResourceQuotas(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("ResourceQuota list is not accessible: %v", err)), nil
	}
	if len(quotaList.Items) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	perPod := target.Workload.PodRequests()
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	var findings []model.Finding
	for _, q := range quotaList.Items {
		resourceRef := model.ResourceRef{Kind: "ResourceQuota", Namespace: q.Namespace, Name: q.Name}
		if f, ok := hpaPodCountFinding(q, resourceRef, workloadRef, targeting.Name, targeting.Spec.MaxReplicas, additional); ok {
			findings = append(findings, f)
		}
		for _, resName := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if f, ok := hpaComputeFinding(q, resourceRef, workloadRef, targeting.Name, targeting.Spec.MaxReplicas, additional, resName, perPod[resName]); ok {
				findings = append(findings, f)
			}
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

func hpaPodCountFinding(q corev1.ResourceQuota, resourceRef model.ResourceRef, workloadRef, hpaName string, maxReplicas int32, additional int64) (model.Finding, bool) {
	hard, used, ok := headroom(q, corev1.ResourcePods, "")
	if !ok {
		return model.Finding{}, false
	}
	available := hard.Value() - used.Value()
	if available >= additional {
		return model.Finding{}, false
	}
	return model.Finding{
		CheckID:  HPAQuotaHeadroomCheckID,
		Severity: model.SeverityMedium,
		Cause: fmt.Sprintf(
			"HorizontalPodAutoscaler %q can scale %s up to %d replicas (%d more than the current desired count), but ResourceQuota %q leaves headroom for only %d additional pods: if the HPA scales up during a rollout, new pods will be blocked the same way surge would be",
			hpaName, workloadRef, maxReplicas, additional, q.Name, available,
		),
		Evidence: []string{
			fmt.Sprintf("hpaMaxReplicas=%d additionalPods=%d", maxReplicas, additional),
			fmt.Sprintf("quota pods hard=%s used=%s", hard.String(), used.String()),
		},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("increase the \"pods\" quota of %q, lower HorizontalPodAutoscaler %q's maxReplicas, or free capacity by terminating other workloads; the correct choice depends on who shares the namespace", q.Name, hpaName),
			ContextDependent: true,
		},
		Resource: resourceRef,
	}, true
}

func hpaComputeFinding(q corev1.ResourceQuota, resourceRef model.ResourceRef, workloadRef, hpaName string, maxReplicas int32, additional int64, resName corev1.ResourceName, perPod resource.Quantity) (model.Finding, bool) {
	hard, used, ok := headroom(q, resName, "requests.")
	if !ok || perPod.IsZero() {
		return model.Finding{}, false
	}
	available := hard.DeepCopy()
	available.Sub(used)
	needed := *resource.NewMilliQuantity(perPod.MilliValue()*additional, perPod.Format)
	if available.Cmp(needed) >= 0 {
		return model.Finding{}, false
	}
	return model.Finding{
		CheckID:  HPAQuotaHeadroomCheckID,
		Severity: model.SeverityMedium,
		Cause: fmt.Sprintf(
			"HorizontalPodAutoscaler %q can scale %s up to %d replicas, requiring up to %s of additional %s, but ResourceQuota %q leaves only %s of headroom: if the HPA scales up during a rollout, new pods will be blocked the same way surge would be",
			hpaName, workloadRef, maxReplicas, needed.String(), resName, q.Name, available.String(),
		),
		Evidence: []string{
			fmt.Sprintf("hpaMaxReplicas=%d additionalPods=%d perPodRequest.%s=%s", maxReplicas, additional, resName, perPod.String()),
			fmt.Sprintf("quota %s hard=%s used=%s", resName, hard.String(), used.String()),
		},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("increase the %s quota on %q, lower HorizontalPodAutoscaler %q's maxReplicas, or reduce container requests; the correct choice depends on how much extra capacity the namespace can provide", resName, q.Name, hpaName),
			ContextDependent: true,
		},
		Resource: resourceRef,
	}, true
}
