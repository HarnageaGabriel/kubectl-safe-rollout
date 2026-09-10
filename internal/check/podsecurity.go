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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	psaapi "k8s.io/pod-security-admission/api"
	psapolicy "k8s.io/pod-security-admission/policy"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// PodSecurityAdmissionCheckID is the stable identifier for this check.
const PodSecurityAdmissionCheckID = "pod-security-admission"

// PodSecurityAdmission checks whether this workload's pod template would be
// rejected in admission by the in-tree PodSecurity plugin, given the level
// the target namespace actually enforces. Like limitrange-feasibility, this
// is a namespace policy that blocks pod creation, not the rollout itself: a
// fact verifiable only against live namespace state, long before `watch`
// would see anything.
//
// Only the pod-security.kubernetes.io/enforce label gates a finding. The
// enforce-version label is read alongside it to build the evaluated policy
// version. warn / audit (and their -version labels) are deliberately never
// turned into a finding: a `warn` violation is already surfaced to the user
// as a header at `kubectl apply` time (the same scoping reasoning applied to
// admission-webhook-visibility's UPDATE exclusion), and `audit` only writes
// an annotation. A namespace with no enforce label at all produces NO
// finding — not a Skip (which would fire on the large majority of namespaces
// and turn a real signal into noise) and not a Low (generic hygiene already
// covered by offline linters, which this project deprioritizes).
//
// The asymmetry that makes a pre-flight check worthwhile, verified live on
// kind (v1.37.0): a namespace labelled
// pod-security.kubernetes.io/enforce: restricted with a non-conformant pod
// template lets `kubectl apply` of the Deployment SUCCEED — only a
// `Warning: would violate PodSecurity "restricted:latest": ...` header is
// printed, not an error — while ZERO pods are ever created and the
// ReplicaSet carries a FailedCreate event with, verbatim:
//
//	Error creating: pods "bad-..." is forbidden: violates PodSecurity
//	"restricted:latest": allowPrivilegeEscalation != false (container
//	"app" must set securityContext.allowPrivilegeEscalation=false),
//	unrestricted capabilities (container "app" must set
//	securityContext.capabilities.drop=["ALL"]), runAsNonRoot != true (pod
//	or container "app" must set securityContext.runAsNonRoot=true),
//	seccompProfile (pod or container "app" must set
//	securityContext.seccompProfile.type to "RuntimeDefault" or
//	"Localhost")
//
// That invisibility at apply time is the entire justification for this
// check.
//
// Severity is only ever High: a namespace enforcing baseline or restricted,
// and the vendored k8s.io/pod-security-admission evaluator returning
// Allowed == false for the pod template at that level. This is a
// deterministic in-admission rejection by an in-tree plugin — no retry, no
// feature gate — the same standard as serviceaccount-exists,
// priorityclass-exists and limitrange-feasibility. There is no Medium tier:
// a version skew (enforce-version naming a minor newer than the vendored
// ruleset) does not become Medium; the evaluated policy version is simply
// named in the evidence (e.g. evaluatedPolicyVersion=restricted:latest), and
// because this tool never calls Discovery().ServerVersion(), that version
// may not be identical to the apiserver's — a declared approximation, the
// project's usual style. There is no Low tier. One finding per workload, not
// per container: PodSecurity rejects the whole Pod and the evaluator returns
// a single aggregate verdict; every reason it gives goes into the evidence,
// one line each.
//
// Known blind spots, declared rather than hidden:
//
//   - The cluster-wide default level, and the exemptions block
//     (usernames / runtimeClassNames / namespaces), live in an
//     AdmissionConfiguration file on the apiserver
//     (PodSecurityConfiguration), not in any API object this tool can read.
//     A namespace with no enforce label may still be enforcing a non-
//     privileged default; a labelled namespace may be exempt and therefore
//     not enforcing at all. Both are accepted false negatives.
//   - The pod template is evaluated as written. The PodSpec fields the Pod
//     Security Standards inspect (hostNetwork/hostPID/hostIPC, hostPath
//     volumes, hostPort, privileged, capabilities,
//     allowPrivilegeEscalation, runAsNonRoot/runAsUser, seccompProfile,
//     seLinuxOptions, procMount, sysctls, appArmorProfile and the legacy
//     annotations) are NOT touched by SetDefaults_Pod, so unlike
//     limitrange-feasibility there is no stage-1 defaulting copy to
//     simulate. The real Pod can still diverge from the template only
//     through: a mutating admission webhook (the significant case —
//     admission-webhook-visibility already reports a matching mutating
//     webhook at Low as the declared escape hatch; this check does not
//     re-detect it); the projected ServiceAccount token volume injected by
//     the ServiceAccount plugin; the persistentVolumeClaim volumes a
//     StatefulSet controller injects per ordinal; and the per-node
//     nodeAffinity / extra tolerations a DaemonSet controller injects.
//     None of those last four touch a field the Pod Security Standards
//     evaluate, so none produces a false positive here, and no workload
//     kind is excluded.
//   - An enforce label whose value does not parse ("bogus", etc.) is
//     handled defensively (the check Skips) but is unreachable on a real
//     cluster: the apiserver itself rejects such a value at namespace
//     write time
//     ("metadata.labels[pod-security.kubernetes.io/enforce]: Invalid
//     value: \"bogus\": must be one of privileged, baseline, restricted"),
//     the same style already used for limitrange-feasibility's max-only
//     case.
type PodSecurityAdmission struct{}

// ID implements check.Check.
func (PodSecurityAdmission) ID() string { return PodSecurityAdmissionCheckID }

// Run implements check.Check. The Namespace Get is the first, unconditional
// operation: it is the check's only live read, so a restricted-RBAC run
// exercises the degrade path with no dedicated fixture, the same as
// limitrange-feasibility and pdb-eviction-blocked.
func (c PodSecurityAdmission) Run(ctx context.Context, target Target) (Result, error) {
	ns, err := target.Client.CoreV1().Namespaces().Get(ctx, target.Namespace, metav1.GetOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("namespace %q is not readable: %v", target.Namespace, err)), nil
	}

	rawLevel, ok := ns.Labels[psaapi.EnforceLevelLabel]
	if !ok || rawLevel == "" {
		// No enforce label: no finding (see the type's doc comment for why
		// this is not a Skip and not a Low).
		return Result{CheckID: c.ID()}, nil
	}

	level, err := psaapi.ParseLevel(rawLevel)
	if err != nil {
		// Defensive: unreachable on a real cluster, the apiserver rejects an
		// invalid enforce label value at write time.
		return Skip(c.ID(), fmt.Sprintf("namespace %q has an unparseable %s label %q: %v", target.Namespace, psaapi.EnforceLevelLabel, rawLevel, err)), nil
	}
	if level == psaapi.LevelPrivileged {
		// The privileged level admits every pod; return early rather than
		// rely on the evaluator to say so.
		return Result{CheckID: c.ID()}, nil
	}

	policyVersion := psaapi.LatestVersion()
	if rawVersion, ok := ns.Labels[psaapi.EnforceVersionLabel]; ok && rawVersion != "" && rawVersion != psaapi.VersionLatest {
		if v, verr := psaapi.ParseVersion(rawVersion); verr == nil {
			policyVersion = v
		}
		// On a parse error, fall back to the latest vendored version: the
		// apiserver validates this label too, so an unparseable value is
		// unreachable on a real cluster (same reasoning as the enforce
		// label above).
	}
	lv := psaapi.LevelVersion{Level: level, Version: policyVersion}

	evaluator, err := psapolicy.NewEvaluator(psapolicy.DefaultChecks(), nil)
	if err != nil {
		// DefaultChecks() is a static, always-valid set: a failure here is a
		// bug in this code or the vendored library, not an expected cluster
		// condition, so it is an error rather than a Skip (rules/go.md).
		return Result{}, fmt.Errorf("building the pod security evaluator: %w", err)
	}

	tmpl := target.Workload.PodTemplate()
	agg := psapolicy.AggregateCheckResults(evaluator.EvaluatePod(lv, &tmpl.ObjectMeta, &tmpl.Spec))
	if agg.Allowed {
		return Result{CheckID: c.ID()}, nil
	}

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	evidence := []string{
		fmt.Sprintf("enforceLevel=%s", level),
		fmt.Sprintf("evaluatedPolicyVersion=%s", lv.String()),
	}
	for i, reason := range agg.ForbiddenReasons {
		detail := ""
		if i < len(agg.ForbiddenDetails) {
			detail = agg.ForbiddenDetails[i]
		}
		if detail != "" {
			evidence = append(evidence, fmt.Sprintf("%s (%s)", reason, detail))
		} else {
			evidence = append(evidence, reason)
		}
	}

	finding := model.Finding{
		CheckID:  c.ID(),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"namespace %q enforces Pod Security Admission at level %q, and the pod template of workload %s would be rejected at that level: pod creation is denied in admission, so a rollout creates zero pods even though `kubectl apply` of the workload succeeds with only a warning",
			target.Namespace, level, workloadRef,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"namespace %q enforces the %q Pod Security Standard; the correct fix depends on whether the workload manifest is wrong (harden the pod template's securityContext to satisfy %q, per the reasons in the evidence), the namespace's pod-security.kubernetes.io/enforce label is stricter than intended (relax it), or this workload belongs in a different namespace",
				target.Namespace, level, level,
			),
			Commands:         []string{fmt.Sprintf("kubectl get namespace %s -o jsonpath='{.metadata.labels}'", target.Namespace)},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{
			Kind:      target.Workload.Kind(),
			Namespace: target.Namespace,
			Name:      target.Workload.Name(),
		},
	}

	return Result{CheckID: c.ID(), Findings: []model.Finding{finding}}, nil
}
