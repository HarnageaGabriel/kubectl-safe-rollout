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

package check_test

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// capacityDeployment builds on the shared deployment() helper (pdb_test.go)
// so every node-capacity-feasibility fixture uses the same pod labels as
// the rest of the suite.
func capacityDeployment(mutate func(*corev1.PodSpec)) *appsv1.Deployment {
	d := deployment(3, noSurgeStrategy())
	if mutate != nil {
		mutate(&d.Spec.Template.Spec)
	}
	return d
}

func resourceList(cpu, memory string) corev1.ResourceList {
	list := corev1.ResourceList{}
	if cpu != "" {
		list[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if memory != "" {
		list[corev1.ResourceMemory] = resource.MustParse(memory)
	}
	return list
}

func containerWithRequests(name, cpu, memory string) corev1.Container {
	return corev1.Container{Name: name, Resources: corev1.ResourceRequirements{Requests: resourceList(cpu, memory)}}
}

func containerWithLimits(name, cpu, memory string) corev1.Container {
	return corev1.Container{Name: name, Resources: corev1.ResourceRequirements{Limits: resourceList(cpu, memory)}}
}

// withAllocatable returns a node mutator that sets Status.Allocatable.
func withAllocatable(cpu, memory string) func(*corev1.Node) {
	return func(n *corev1.Node) {
		n.Status.Allocatable = resourceList(cpu, memory)
	}
}

// withAllocatableList mirrors withAllocatable but takes an already-built
// ResourceList, for the degradation test that needs an allocatable map
// missing a key entirely rather than one built via resourceList's cpu/mem
// shorthand.
func withAllocatableList(list corev1.ResourceList) func(*corev1.Node) {
	return func(n *corev1.Node) {
		n.Status.Allocatable = list
	}
}

func runNodeCapacityCheck(t *testing.T, w workload.Workload, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	res, err := check.NodeCapacityFeasibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  w,
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

// --- High ---

func TestNodeCapacityFeasibility_CPUExceedsAllCandidateNodes_HighFinding(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "10", "")}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatable("6", "")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.CheckID != check.NodeCapacityFeasibilityCheckID {
		t.Errorf("checkID = %q, want %q", f.CheckID, check.NodeCapacityFeasibilityCheckID)
	}
	if !evidenceContains(f.Evidence, "requested.cpu=10") || !evidenceContains(f.Evidence, "largestAllocatable.cpu=6") {
		t.Errorf("evidence must name the requested and largest allocatable cpu values, got %+v", f.Evidence)
	}
	if !f.Remediation.ContextDependent {
		t.Errorf("remediation must declare itself context-dependent")
	}
}

func TestNodeCapacityFeasibility_MemoryExceedsAllCandidateNodes_HighFinding(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "", "16Gi")}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatable("", "8Gi")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if !evidenceContains(f.Evidence, "requested.memory=16Gi") || !evidenceContains(f.Evidence, "largestAllocatable.memory=8Gi") {
		t.Errorf("evidence must name the requested and largest allocatable memory values, got %+v", f.Evidence)
	}
}

func TestNodeCapacityFeasibility_BothCPUAndMemoryExceed_TwoFindings(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "10", "16Gi")}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatable("6", "8Gi")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 2 {
		t.Fatalf("want exactly 2 findings, got %+v", res)
	}
	sawCPU, sawMemory := false, false
	for _, f := range res.Findings {
		if f.Severity != model.SeverityHigh {
			t.Errorf("severity = %v, want High for every finding", f.Severity)
		}
		if evidenceContains(f.Evidence, "requested.cpu=") {
			sawCPU = true
		}
		if evidenceContains(f.Evidence, "requested.memory=") {
			sawMemory = true
		}
	}
	if !sawCPU || !sawMemory {
		t.Errorf("want one finding for cpu and one for memory, got %+v", res.Findings)
	}
}

func TestNodeCapacityFeasibility_NodeSelectorConfinesToSmallPool_HighFinding(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.NodeSelector = map[string]string{"tier": "web"}
		spec.Containers = []corev1.Container{containerWithRequests("app", "4", "")}
	})
	nodes := nodeObjects(
		testNode("node-a", map[string]string{"tier": "web"}, withAllocatable("2", "")),
		testNode("node-b", map[string]string{"tier": "other"}, withAllocatable("100", "")),
	)
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: the big non-matching node must not be considered", f.Severity)
	}
	if !evidenceContains(f.Evidence, "largestAllocatable.cpu=2 node=node-a") {
		t.Errorf("evidence must name node-a (2 cpu), not the bigger non-matching node-b, got %+v", f.Evidence)
	}
}

func TestNodeCapacityFeasibility_LimitsOnlyNoRequests_HighFinding(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithLimits("app", "10", "")}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatable("6", "")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding (stage-1 limits->requests copy simulated), got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

func TestNodeCapacityFeasibility_OversizedInitContainer_HighFinding(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.InitContainers = []corev1.Container{containerWithRequests("init", "10", "")}
		spec.Containers = []corev1.Container{containerWithRequests("app", "100m", "")}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatable("6", "")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("an oversized init container must be included in the aggregate: want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// A single node exists, big but still insufficient for the request, and is
// both cordoned and tainted: proves DaemonSet evaluates against the
// superset (allCandidates, label match only) rather than the filtered
// candidates set scheduling-constraints-feasibility uses. If the filtered
// set were used here, the candidate set would be empty and this check
// would wrongly emit nothing.
func TestNodeCapacityFeasibility_DaemonSet_OversizedAgainstCordonedTaintedNode_HighFinding(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: testNamespace},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: podLabels()},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels()},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{containerWithRequests("app", "10", "")},
				},
			},
		},
		Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 1},
	}
	nodes := nodeObjects(
		testNode("node-a", nil, withAllocatable("6", ""), cordoned, tainted("dedicated", "gpu", corev1.TaintEffectNoSchedule)),
	)
	res := runNodeCapacityCheck(t, workload.FromDaemonSet(ds), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// --- no finding ---

func TestNodeCapacityFeasibility_RequestFitsEveryNode_NoFindings(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "100m", "64Mi")}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatable("1", "512Mi")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a comfortable request must produce no findings, got %+v", res)
	}
}

func TestNodeCapacityFeasibility_FitsAtLeastOneCandidate_NoFindings(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "2", "")}
	})
	nodes := nodeObjects(
		testNode("node-a", nil, withAllocatable("1", "")),
		testNode("node-b", nil, withAllocatable("10", "")),
	)
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("fitting on at least one candidate node is enough: want 0 findings, got %+v", res)
	}
}

func TestNodeCapacityFeasibility_NoCPUOrMemoryRequests_NoFindingsNoAPICall(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{{Name: "app"}}
	})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatal("must not list Nodes when no cpu/memory request is declared")
		return false, nil, nil
	})
	res, err := check.NodeCapacityFeasibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("want empty result when no cpu/memory request exists at all, got %+v", res)
	}
}

// Guard against a naive implementation that would sum the init container's
// request onto the regular containers' sum instead of taking the max: the
// naive sum (1100m) would exceed the node's allocatable (900m), but the
// correct max (800m) fits comfortably.
func TestNodeCapacityFeasibility_InitContainerSmallerThanRegularSum_NoFindings(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.InitContainers = []corev1.Container{containerWithRequests("init", "300m", "")}
		spec.Containers = []corev1.Container{
			containerWithRequests("app1", "400m", ""),
			containerWithRequests("app2", "400m", ""),
		}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatable("900m", "")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("the init container's contribution must be a max, not an addend: want 0 findings, got %+v", res)
	}
}

func TestNodeCapacityFeasibility_RequestEqualsAllocatable_NoFindings(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "1", "")}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatable("1", "")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("the scheduler's fit check uses a strict '>' comparison: an exact match must not be flagged, got %+v", res)
	}
}

func TestNodeCapacityFeasibility_NonDefaultScheduler_NoFindingsNoAPICall(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.SchedulerName = "custom-scheduler"
		spec.Containers = []corev1.Container{containerWithRequests("app", "10", "")}
	})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		t.Fatal("must not list Nodes for a non-default scheduler")
		return false, nil, nil
	})
	res, err := check.NodeCapacityFeasibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a non-default scheduler must produce an empty result, got %+v", res)
	}
}

func TestNodeCapacityFeasibility_ZeroCandidateNodesAfterFilter_NoFindings(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.NodeSelector = map[string]string{"tier": "gpu"}
		spec.Containers = []corev1.Container{containerWithRequests("app", "10", "")}
	})
	nodes := nodeObjects(testNode("node-a", map[string]string{"tier": "web"}, withAllocatable("6", "")))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("zero candidate nodes is scheduling-constraints-feasibility's fact to report, not this check's: want empty result, got %+v", res)
	}
}

// --- degradation ---

func TestNodeCapacityFeasibility_NodeListForbidden_Skipped(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "1", "")}
	})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing Nodes")
	})
	res, err := check.NodeCapacityFeasibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped || res.SkipReason == "" {
		t.Fatalf("want Skipped=true with a reason, got %+v", res)
	}
}

func TestNodeCapacityFeasibility_EmptyNodeList_Skipped(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "1", "")}
	})
	res := runNodeCapacityCheck(t, workload.FromDeployment(d))
	if !res.Skipped || res.SkipReason == "" {
		t.Fatalf("want Skipped=true with a reason when the cluster reports zero nodes, got %+v", res)
	}
	if len(res.Findings) != 0 {
		t.Errorf("want no findings when skipping, got %+v", res.Findings)
	}
}

func TestNodeCapacityFeasibility_NoCandidateReportsCPUAllocatable_Skipped(t *testing.T) {
	d := capacityDeployment(func(spec *corev1.PodSpec) {
		spec.Containers = []corev1.Container{containerWithRequests("app", "1", "")}
	})
	nodes := nodeObjects(testNode("node-a", nil, withAllocatableList(corev1.ResourceList{})))
	res := runNodeCapacityCheck(t, workload.FromDeployment(d), nodes...)
	if !res.Skipped || res.SkipReason == "" {
		t.Fatalf("no candidate node reporting the requested key must degrade to Skipped, not a misleading High finding, got %+v", res)
	}
	if len(res.Findings) != 0 {
		t.Errorf("want no findings when skipping, got %+v", res.Findings)
	}
}
