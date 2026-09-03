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
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

const (
	zoneKey     = "topology.kubernetes.io/zone"
	hostnameKey = "kubernetes.io/hostname"
)

// schedulingDeployment builds on the shared deployment() helper (pdb_test.go)
// so all scheduling-constraints-feasibility fixtures use the same pod
// labels ("app": "checkout") that self-anti-affinity terms must match.
func schedulingDeployment(replicas int32, strategy appsv1.DeploymentStrategy, mutate func(*corev1.PodSpec)) *appsv1.Deployment {
	d := deployment(replicas, strategy)
	if mutate != nil {
		mutate(&d.Spec.Template.Spec)
	}
	return d
}

// noSurgeStrategy disables surge entirely (maxSurge=0), so tests that are
// not specifically about the surge-deadlock case do not accidentally
// exercise it: a Deployment's zero-value RollingUpdate strategy would
// otherwise default maxSurge/maxUnavailable to 25%, which rounds
// maxUnavailable down to 0 for small replica counts and would spuriously
// trigger the surge branch.
func noSurgeStrategy() appsv1.DeploymentStrategy {
	zero := intstr.FromInt(0)
	one := intstr.FromInt(1)
	return appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxSurge:       &zero,
			MaxUnavailable: &one,
		},
	}
}

func spreadConstraint(topologyKey string, maxSkew int32, whenUnsatisfiable corev1.UnsatisfiableConstraintAction, minDomains *int32) corev1.TopologySpreadConstraint {
	return corev1.TopologySpreadConstraint{
		MaxSkew:           maxSkew,
		TopologyKey:       topologyKey,
		WhenUnsatisfiable: whenUnsatisfiable,
		MinDomains:        minDomains,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: podLabels()},
	}
}

func selfAntiAffinity(topologyKey string) *corev1.Affinity {
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
				TopologyKey:   topologyKey,
				LabelSelector: &metav1.LabelSelector{MatchLabels: podLabels()},
			}},
		},
	}
}

func testNode(name string, labels map[string]string, mutate ...func(*corev1.Node)) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	for _, m := range mutate {
		m(n)
	}
	return n
}

func cordoned(n *corev1.Node) { n.Spec.Unschedulable = true }

func zoneLabels(zone string) map[string]string { return map[string]string{zoneKey: zone} }

func hostnameLabels(name string) map[string]string { return map[string]string{hostnameKey: name} }

func runSchedulingCheck(t *testing.T, w workload.Workload, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	res, err := check.SchedulingConstraintsFeasibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  w,
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

func evidenceContains(evidence []string, substr string) bool {
	for _, e := range evidence {
		if strings.Contains(e, substr) {
			return true
		}
	}
	return false
}

func nodeObjects(nodes ...*corev1.Node) []runtime.Object {
	objs := make([]runtime.Object, 0, len(nodes))
	for _, n := range nodes {
		objs = append(objs, n)
	}
	return objs
}

// --- no-finding (false-positive guard) tests ---

func TestSchedulingConstraintsFeasibility_NoConstraints_NoFindingsNoAPICall(t *testing.T) {
	d := deployment(3, rollingUpdateStrategy(nil))
	client := fake.NewSimpleClientset(d)
	target := check.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d), Client: client}

	res, err := check.SchedulingConstraintsFeasibility{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("want empty result when no topologySpreadConstraints/anti-affinity exist, got %+v", res)
	}
	for _, a := range client.Actions() {
		if a.GetResource().Resource == "nodes" {
			t.Fatalf("must not call the Nodes API when there is nothing to evaluate, got action %+v", a)
		}
	}
}

// The worked example from the design: domains(1) < replicas(3) must NOT be
// treated as infeasible when the eligible domain count still satisfies
// minDomains (1, the implicit default) — this is the regression guard
// against the naive, wrong rule "domains < replicas => infeasible".
func TestSchedulingConstraintsFeasibility_AllNodesInOneZone_MaxSkew1_NoFindings(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, nil),
		}
	})
	nodes := nodeObjects(
		testNode("node-a", zoneLabels("zone-a")),
		testNode("node-b", zoneLabels("zone-a")),
		testNode("node-c", zoneLabels("zone-a")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("1 eligible zone satisfies minDomains=1: must not be infeasible regardless of replica count, got %+v", res)
	}
}

func TestSchedulingConstraintsFeasibility_ThreeZones_MaxSkew1_NoFindings(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, nil),
		}
	})
	nodes := nodeObjects(
		testNode("node-a", zoneLabels("zone-a")),
		testNode("node-b", zoneLabels("zone-b")),
		testNode("node-c", zoneLabels("zone-c")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("3 eligible zones for 3 replicas at maxSkew=1: want 0 findings, got %+v", res)
	}
}

func TestSchedulingConstraintsFeasibility_ScheduleAnywayWithMatchingNodes_NoFindings(t *testing.T) {
	d := schedulingDeployment(5, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.ScheduleAnyway, nil),
		}
	})
	nodes := nodeObjects(testNode("node-a", zoneLabels("zone-a")))
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("ScheduleAnyway is a soft preference and can never block scheduling: want 0 findings, got %+v", res)
	}
}

func TestSchedulingConstraintsFeasibility_AntiAffinityExactFit_NoFindings(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.Affinity = selfAntiAffinity(hostnameKey)
	})
	nodes := nodeObjects(
		testNode("node-a", hostnameLabels("node-a")),
		testNode("node-b", hostnameLabels("node-b")),
		testNode("node-c", hostnameLabels("node-c")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("3 eligible domains for 3 replicas (exact fit): want 0 findings, got %+v", res)
	}
}

func TestSchedulingConstraintsFeasibility_PreferredAntiAffinityOnly_NoFindings(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.Affinity = &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
					Weight: 100,
					PodAffinityTerm: corev1.PodAffinityTerm{
						TopologyKey:   hostnameKey,
						LabelSelector: &metav1.LabelSelector{MatchLabels: podLabels()},
					},
				}},
			},
		}
	})
	nodes := nodeObjects(testNode("node-a", hostnameLabels("node-a")))
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("soft (preferred) anti-affinity never blocks scheduling: want 0 findings, got %+v", res)
	}
}

// Proves the anti-affinity term is filtered out (not just coincidentally
// feasible): the term's capacity (1 node) is far below replicas (5), so if
// it were wrongly evaluated this would be a High finding. A ScheduleAnyway
// spread constraint that IS satisfied is included so the Node List call
// still happens and the run is not merely short-circuiting entirely.
func TestSchedulingConstraintsFeasibility_AntiAffinitySelectorNotOwnPods_NotEvaluated(t *testing.T) {
	d := schedulingDeployment(5, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.ScheduleAnyway, nil),
		}
		spec.Affinity = &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
					TopologyKey:   hostnameKey,
					LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "other-workload"}},
				}},
			},
		}
	})
	nodes := nodeObjects(testNode("node-a", map[string]string{zoneKey: "zone-a", hostnameKey: "node-a"}))
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a term matching a different workload's pods must not be evaluated as self-anti-affinity, got %+v", res)
	}
}

// Guard against a naive implementation that would just not count node-b as
// a domain: instead, the entire term is skipped because a candidate node
// lacks the topologyKey label at all. With replicas=5 and only node-a
// carrying the label, a naive per-node count (domain count 1) would wrongly
// produce a High finding.
func TestSchedulingConstraintsFeasibility_AntiAffinityNodeMissingTopologyKey_NotEvaluated(t *testing.T) {
	d := schedulingDeployment(5, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.Affinity = selfAntiAffinity("rack")
	})
	nodes := nodeObjects(
		testNode("node-a", map[string]string{"rack": "r1"}),
		testNode("node-b", map[string]string{}),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a candidate node lacking the topologyKey must make this check refuse to conclude anything, got %+v", res)
	}
}

// Without the nodeAffinityPolicy=Ignore guard, evaluating this constraint
// against the nodeSelector-narrowed set (1 zone) would wrongly produce a
// High finding (minDomains=3 shortfall); Ignore means the check cannot
// soundly reuse the same candidate-node computation and must not conclude.
func TestSchedulingConstraintsFeasibility_NodeAffinityPolicyIgnoreWithNodeSelector_NotEvaluated(t *testing.T) {
	ignore := corev1.NodeInclusionPolicyIgnore
	d := schedulingDeployment(5, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.NodeSelector = map[string]string{"tier": "web"}
		c := spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, int32Ptr(3))
		c.NodeAffinityPolicy = &ignore
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{c}
	})
	nodes := nodeObjects(
		testNode("node-a", map[string]string{"tier": "web", zoneKey: "zone-a"}),
		testNode("node-b", zoneLabels("zone-b")),
		testNode("node-c", zoneLabels("zone-c")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("nodeAffinityPolicy=Ignore combined with a real nodeSelector must not be evaluated, got %+v", res)
	}
}

func TestSchedulingConstraintsFeasibility_ZeroReplicas_NoFindingsNoAPICall(t *testing.T) {
	d := schedulingDeployment(0, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, nil),
		}
	})
	client := fake.NewSimpleClientset()
	target := check.Target{Namespace: testNamespace, Workload: workload.FromDeployment(d), Client: client}

	res, err := check.SchedulingConstraintsFeasibility{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a workload scaled to zero has nothing to schedule: want empty result, got %+v", res)
	}
	for _, a := range client.Actions() {
		if a.GetResource().Resource == "nodes" {
			t.Fatalf("must not call the Nodes API when replicas is 0, got action %+v", a)
		}
	}
}

// --- Low ---

func TestSchedulingConstraintsFeasibility_ScheduleAnywayDeadConfig_LowFinding(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.ScheduleAnyway, nil),
		}
	})
	nodes := nodeObjects(testNode("node-a", map[string]string{}))
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding for a dead ScheduleAnyway constraint, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityLow {
		t.Errorf("severity = %v, want Low: ScheduleAnyway never blocks scheduling", f.Severity)
	}
	if f.CheckID != check.SchedulingConstraintsFeasibilityCheckID {
		t.Errorf("checkID = %q, want %q", f.CheckID, check.SchedulingConstraintsFeasibilityCheckID)
	}
	if !f.Remediation.ContextDependent {
		t.Errorf("remediation must declare itself context-dependent")
	}
}

// --- Medium ---

func TestSchedulingConstraintsFeasibility_AntiAffinityCordonedNode_MediumFinding(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.Affinity = selfAntiAffinity(hostnameKey)
	})
	nodes := nodeObjects(
		testNode("node-a", hostnameLabels("node-a")),
		testNode("node-b", hostnameLabels("node-b")),
		testNode("node-c", hostnameLabels("node-c"), cordoned),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Fatalf("severity = %v, want Medium: feasible if the cordoned node were uncordoned", f.Severity)
	}
	if !evidenceContains(f.Evidence, "cordonedNodes=1") {
		t.Errorf("evidence must name the cordoned node count, got %+v", f.Evidence)
	}
}

// --- High ---

func TestSchedulingConstraintsFeasibility_SpreadTopologyKeyNoNodes_HighFinding(t *testing.T) {
	d := schedulingDeployment(2, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, nil),
		}
	})
	nodes := nodeObjects(testNode("node-a", map[string]string{}))
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: zero eligible domains, no cordoning involved", res.Findings[0].Severity)
	}
}

func TestSchedulingConstraintsFeasibility_MinDomainsShortfall_HighFinding(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, int32Ptr(3)),
		}
	})
	nodes := nodeObjects(
		testNode("node-a", zoneLabels("zone-a")),
		testNode("node-b", zoneLabels("zone-b")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if !evidenceContains(f.Evidence, "capacity=2") {
		t.Errorf("evidence must show computed capacity 2 (2 eligible domains * maxSkew 1), not replicas(3) or domains*replicas(6), got %+v", f.Evidence)
	}
}

// 6 nodes across 3 zones, but the nodeSelector matches only 2 nodes in a
// single zone: proves the check narrows candidates through nodeSelector
// rather than counting raw node/zone totals (which alone would satisfy
// minDomains=3 and be feasible).
func TestSchedulingConstraintsFeasibility_NodeSelectorNarrowsCandidates_HighFinding(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.NodeSelector = map[string]string{"tier": "web"}
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, int32Ptr(3)),
		}
	})
	nodes := nodeObjects(
		testNode("a1", map[string]string{"tier": "web", zoneKey: "zone-a"}),
		testNode("a2", map[string]string{"tier": "web", zoneKey: "zone-a"}),
		testNode("b1", zoneLabels("zone-b")),
		testNode("b2", zoneLabels("zone-b")),
		testNode("c1", zoneLabels("zone-c")),
		testNode("c2", zoneLabels("zone-c")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: nodeSelector narrows the eligible domains to 1, below minDomains=3", res.Findings[0].Severity)
	}
}

// Same narrowing as the nodeSelector case, but via required nodeAffinity
// matchExpressions(In): proves the component-helpers nodeaffinity path is
// actually wired in, not just plain nodeSelector matching.
func TestSchedulingConstraintsFeasibility_NodeAffinityNarrowsCandidates_HighFinding(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.Affinity = &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key: "tier", Operator: corev1.NodeSelectorOpIn, Values: []string{"web"},
						}},
					}},
				},
			},
		}
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, int32Ptr(3)),
		}
	})
	nodes := nodeObjects(
		testNode("a1", map[string]string{"tier": "web", zoneKey: "zone-a"}),
		testNode("a2", map[string]string{"tier": "web", zoneKey: "zone-a"}),
		testNode("b1", zoneLabels("zone-b")),
		testNode("b2", zoneLabels("zone-b")),
		testNode("c1", zoneLabels("zone-c")),
		testNode("c2", zoneLabels("zone-c")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: required nodeAffinity narrows the eligible domains to 1, below minDomains=3", res.Findings[0].Severity)
	}
}

func TestSchedulingConstraintsFeasibility_AntiAffinityReplicasExceedNodes_HighFinding(t *testing.T) {
	d := schedulingDeployment(4, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.Affinity = selfAntiAffinity(hostnameKey)
	})
	nodes := nodeObjects(
		testNode("node-a", hostnameLabels("node-a")),
		testNode("node-b", hostnameLabels("node-b")),
		testNode("node-c", hostnameLabels("node-c")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: 3 eligible domains cannot host 4 replicas", res.Findings[0].Severity)
	}
}

// Capacity (3) fits replicas (3) exactly, but not the surge peak (4) with
// maxUnavailable=0: the deterministic surge deadlock from decision 7.
func TestSchedulingConstraintsFeasibility_AntiAffinitySurgeDeadlock_HighFinding(t *testing.T) {
	one := intstr.FromInt(1)
	zero := intstr.FromInt(0)
	strategy := appsv1.DeploymentStrategy{
		Type: appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{
			MaxSurge:       &one,
			MaxUnavailable: &zero,
		},
	}
	d := schedulingDeployment(3, strategy, func(spec *corev1.PodSpec) {
		spec.Affinity = selfAntiAffinity(hostnameKey)
	})
	nodes := nodeObjects(
		testNode("node-a", hostnameLabels("node-a")),
		testNode("node-b", hostnameLabels("node-b")),
		testNode("node-c", hostnameLabels("node-c")),
	)
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: capacity fits replicas but not the surge peak with maxUnavailable=0", res.Findings[0].Severity)
	}
}

func TestSchedulingConstraintsFeasibility_ZeroCandidateNodes_HighFinding(t *testing.T) {
	d := schedulingDeployment(2, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.NodeSelector = map[string]string{"tier": "gpu"}
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, nil),
		}
	})
	nodes := nodeObjects(testNode("node-a", zoneLabels("zone-a")))
	res := runSchedulingCheck(t, workload.FromDeployment(d), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if !evidenceContains(f.Evidence, "matchingNodes=0") {
		t.Errorf("evidence must state zero matching nodes, got %+v", f.Evidence)
	}
}

// --- degradation ---

func TestSchedulingConstraintsFeasibility_NodeListForbidden_Skipped(t *testing.T) {
	d := schedulingDeployment(3, noSurgeStrategy(), func(spec *corev1.PodSpec) {
		spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{
			spreadConstraint(zoneKey, 1, corev1.DoNotSchedule, nil),
		}
	})
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies listing Nodes")
	})

	res, err := check.SchedulingConstraintsFeasibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatal("want Skipped=true when the Node list is not accessible")
	}
	if res.SkipReason == "" {
		t.Errorf("SkipReason must not be empty")
	}
}

// --- StatefulSet ---

// StatefulSet has no MaxSurge field at all (workload.UpdateStrategy always
// leaves it nil for StatefulSet), so surgeCount() returns ok=false and the
// surge-deadlock branch structurally never applies: only the plain
// capacity<replicas comparison can fire, proven here by capacity(3) <
// replicas(4).
func TestSchedulingConstraintsFeasibility_StatefulSet_AntiAffinityInfeasible_HighFinding(t *testing.T) {
	replicas := int32(4)
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: podLabels()},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels()},
				Spec: corev1.PodSpec{
					Affinity: selfAntiAffinity(hostnameKey),
				},
			},
		},
	}
	nodes := nodeObjects(
		testNode("node-a", hostnameLabels("node-a")),
		testNode("node-b", hostnameLabels("node-b")),
		testNode("node-c", hostnameLabels("node-c")),
	)
	res := runSchedulingCheck(t, workload.FromStatefulSet(s), nodes...)
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: 3 eligible domains cannot host 4 replicas", f.Severity)
	}
	if !strings.Contains(f.Cause, "StatefulSet/db") {
		t.Errorf("cause must name the StatefulSet workload, got %q", f.Cause)
	}
}
