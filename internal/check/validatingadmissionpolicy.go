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
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ValidatingAdmissionPolicyVisibilityCheckID is the stable identifier for
// this check.
const ValidatingAdmissionPolicyVisibilityCheckID = "validating-admission-policy-visibility"

// ValidatingAdmissionPolicyVisibility is the CEL-native sibling of
// AdmissionWebhookVisibility: it surfaces ValidatingAdmissionPolicy +
// ValidatingAdmissionPolicyBinding pairs that sit in this workload's
// pod-creation path, using only facts the vendored API types and the
// vendored apiserver admission plugin guarantee. It never executes CEL.
//
// Both types are cluster-scoped
// (k8s.io/api@v0.37.0/admissionregistration/v1/types.go:137 and :725,
// `+genclient:nonNamespaced` on both).
//
// Two mechanism facts anchor every conclusion below, both split across the
// policy and the binding rather than living on one object the way a
// webhook's failurePolicy does:
//
//   - failurePolicy (types.go:238-252) lives on the POLICY and governs CEL
//     parse/type/runtime errors and misconfiguration only ("failurePolicy
//     does not define how validations that evaluate to false are
//     handled", types.go:245). Defaults to Fail.
//   - validationActions (types.go:501-543) lives on the BINDING: Deny,
//     Warn, Audit (Deny+Warn mutually exclusive). Only Deny actually
//     blocks pod creation — verified against
//     k8s.io/apiserver@v0.36.1/pkg/admission/plugin/policy/validating/dispatcher.go:233-249,
//     where ActionDeny is appended to the request's denied decisions only
//     under the `case admissionregistrationv1.Deny:` branch of the
//     per-binding validationActions loop; Warn/Audit-only actions never
//     reach that branch.
//
// A CEL runtime/eval error under failurePolicy=Fail becomes exactly that
// same ActionDeny — verified against
// k8s.io/apiserver@v0.36.1/pkg/admission/plugin/policy/validating/validator.go:64-69
// (policyDecisionActionForError: Ignore -> ActionAdmit, anything else ->
// ActionDeny) and :159-162 (an expression evaluation error takes that
// path). This is the mechanism F2 below reports.
//
// F2's specific trigger — paramKind set on the policy, no paramRef on the
// binding — does not error at configuration time; it silently binds
// `params` to null. Verified against
// k8s.io/apiserver@v0.36.1/pkg/admission/plugin/policy/generic/policy_dispatcher.go:320-323
// (`case paramRef == nil: // Policy ParamKind is set, but binding does not
// use it. Validate with nil params`), matching the API's own doc comment
// (types.go:220: "If paramKind is specified but paramRef is unset in
// ValidatingAdmissionPolicyBinding, the params variable will be null.").
// Any validation expression that then dereferences a field of `params`
// raises a CEL runtime error ("no such key: ..."), which, under
// failurePolicy=Fail, is the ActionDeny path above. Whether any
// expression actually does that is a fact only reading CEL could
// establish, which this check declines to do — hence F2 is Medium, not
// High.
//
// Deliberately never used as a signal, for reasons stated once here:
//
//   - The CEL expressions themselves are never executed. In VAP's CEL
//     environment `object` is bound as a POST-mutation, POST-defaulting
//     Pod — materially different from Workload.PodTemplate() (no
//     generated name, no injected ServiceAccount volume, no
//     LimitRange-defaulted resources, no PriorityClass admission output).
//     Evaluating CEL against the template this project reasons about
//     elsewhere risks a false-positive Deny verdict this tool would then
//     report as fact.
//   - status.typeChecking is never read. It is an advisory lint against a
//     resolved OpenAPI schema (ValidatingAdmissionPolicyStatus.TypeChecking,
//     types.go:163-166), unrelated to admission-time evaluation — `object`
//     is CEL DynType there too, so a clean typeChecking result says
//     nothing about what a live pod would actually contain.
//
// Matching semantics, all from
// k8s.io/apiserver@v0.36.1/pkg/admission/plugin/policy/matching/matching.go,
// used identically for both the policy's matchConstraints and the
// binding's matchResources (both are the same MatchResources type,
// dispatched through the same MatchCriteria interface, matching.go:35-40):
//
//   - excludeResourceRules wins over resourceRules: matching.go:89-91.
//   - an EMPTY resourceRules list means "matches everything", not
//     "matches nothing" — matching.go:99-102. This is the easiest fact
//     here to get backwards, so it is stated explicitly: a binding that
//     sets matchResources but leaves resourceRules unset constrains
//     nothing further, consistent with the field's own doc comment
//     (types.go: "When resourceRules is unset, it does not constrain
//     resource matching.").
//   - a rule (include or exclude) whose resourceNames is non-empty can
//     never usefully match a controller-created pod, which is always
//     admitted under a generated name: matching.go's own matcher requires
//     an exact name equality against ResourceNames once the rule shape
//     matches (matching.go:137-142, including an upstream TODO
//     acknowledging the generated-name edge case). This check therefore
//     treats any such rule, on either side, as never matching: an include
//     rule like this excludes that rule from every conclusion, and an
//     exclude rule like this can never actually exclude a
//     controller-created pod.
//   - matchPolicy Exact vs Equivalent is not modeled distinctly, for the
//     same reason already stated in admissionwebhook.go: pods are always
//     core/v1, so the distinction never changes the answer for a Pod.
//
// matchConditions (types.go:262-282, CEL, on the POLICY only — bindings
// have no equivalent field) are never evaluated. A policy that declares
// any is excluded from every conclusion below, identical treatment and
// reasoning to admissionwebhook.go's webhook-level matchConditions.
//
// A binding naming a policyName that does not exist is silently ignored
// by the apiserver (types.go:480-481) and produces no finding here
// either.
//
// ValidatingAdmissionPolicy reached v1/GA in Kubernetes 1.30
// (+k8s:prerelease-lifecycle-gen:introduced=1.30 on both types). On an
// older cluster the List itself 404s; this check reports that as a
// distinct Skip reason from an RBAC Forbidden, naming the API rather than
// implying a permission problem that changing RBAC could fix.
//
// Findings:
//
//   - F1, Low: a policy's matchConstraints match this workload's
//     pod/CREATE admission, a binding referencing that policy also
//     matches, and that binding's validationActions include Deny. One
//     finding per (policy, binding) pair, uncapped: this tool has no
//     verifiable fact ranking one pair above another. This is inventory
//     ("this policy is in the path"), not a promise that any validation
//     will actually fail — this check cannot evaluate that.
//   - F2, Medium: everything F1 requires, PLUS the policy's paramKind is
//     set, the matching binding's paramRef is unset, and the policy's
//     effective failurePolicy is Fail. Scored above F1 because the
//     params-is-null mechanism is a verified fact, not scored as High
//     because whether any validation expression actually dereferences
//     params is unknowable without reading CEL.
//
// No High severity exists in this check's v1: unlike
// AdmissionWebhookVisibility's backend-unreachable facts, nothing here is
// a guaranteed block independent of what the policy's own CEL decides.
//
// Deferred, not built here:
//
//   - a High-severity finding for paramKind naming a Kind this tool
//     cannot resolve (would require an extra, broader discovery call this
//     check does not otherwise need).
//   - MutatingAdmissionPolicy, the CEL-native mutating counterpart in the
//     same API group.
type ValidatingAdmissionPolicyVisibility struct{}

// ID implements check.Check.
func (ValidatingAdmissionPolicyVisibility) ID() string {
	return ValidatingAdmissionPolicyVisibilityCheckID
}

// Run implements check.Check.
func (c ValidatingAdmissionPolicyVisibility) Run(ctx context.Context, target Target) (Result, error) {
	policyList, err := target.Client.AdmissionregistrationV1().ValidatingAdmissionPolicies().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), vapListSkipReason("validatingadmissionpolicy", err)), nil
	}
	bindingList, err := target.Client.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), vapListSkipReason("validatingadmissionpolicybinding", err)), nil
	}

	podLabels := target.Workload.PodLabels()

	// Phase 1: which policies structurally match this workload's
	// pod/CREATE path, ignoring namespaceSelector for now (mirrors
	// admissionwebhook.go's two-phase approach so a Namespace read
	// failure only Skips this check when something actually needs it).
	policiesByName := make(map[string]admissionregistrationv1.ValidatingAdmissionPolicy)
	for _, p := range policyList.Items {
		if len(p.Spec.MatchConditions) > 0 {
			continue
		}
		if p.Spec.MatchConstraints == nil {
			// Required field on a real object; defensive only.
			continue
		}
		match, err := matchResourcesMatchesPodExceptNamespace(p.Spec.MatchConstraints, podLabels)
		if err != nil || !match {
			continue
		}
		policiesByName[p.Name] = p
	}

	type vapPair struct {
		policy  admissionregistrationv1.ValidatingAdmissionPolicy
		binding admissionregistrationv1.ValidatingAdmissionPolicyBinding
	}
	var pairs []vapPair
	for _, b := range bindingList.Items {
		p, ok := policiesByName[b.Spec.PolicyName]
		if !ok {
			// Dangling policyName or a policy that did not structurally
			// match: both silent, matching the apiserver's own treatment
			// of a dangling policyName (types.go:480-481).
			continue
		}
		match, err := matchResourcesMatchesPodExceptNamespace(b.Spec.MatchResources, podLabels)
		if err != nil || !match {
			continue
		}
		pairs = append(pairs, vapPair{policy: p, binding: b})
	}

	needsNamespace := false
	for _, pair := range pairs {
		if matchResourcesHasNamespaceSelector(pair.policy.Spec.MatchConstraints) || matchResourcesHasNamespaceSelector(pair.binding.Spec.MatchResources) {
			needsNamespace = true
			break
		}
	}

	var namespaceLabels map[string]string
	if needsNamespace {
		ns, err := target.Client.CoreV1().Namespaces().Get(ctx, target.Namespace, metav1.GetOptions{})
		if err != nil {
			return Skip(c.ID(), fmt.Sprintf("namespace %q is not accessible, and at least one matching ValidatingAdmissionPolicy/ValidatingAdmissionPolicyBinding pair has a non-empty namespaceSelector: %v", target.Namespace, err)), nil
		}
		namespaceLabels = ns.Labels
	}

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	describeWorkloadCmd := fmt.Sprintf("kubectl describe %s %s -n %s", strings.ToLower(target.Workload.Kind()), target.Workload.Name(), target.Namespace)

	var findings []model.Finding
	for _, pair := range pairs {
		if match, err := matchResourcesNamespaceMatches(pair.policy.Spec.MatchConstraints, namespaceLabels); err != nil || !match {
			continue
		}
		if match, err := matchResourcesNamespaceMatches(pair.binding.Spec.MatchResources, namespaceLabels); err != nil || !match {
			continue
		}
		if !containsValidationAction(pair.binding.Spec.ValidationActions, admissionregistrationv1.Deny) {
			continue
		}
		findings = append(findings, vapFinding(pair.policy, pair.binding, workloadRef, describeWorkloadCmd))
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// vapListSkipReason distinguishes an API-not-served 404 (this cluster's
// apiserver has no admissionregistration.k8s.io/v1 endpoint for resource,
// meaning the cluster predates Kubernetes 1.30) from any other read
// failure, most commonly RBAC Forbidden. Conflating the two would tell an
// operator to fix a permission that does not exist to fix.
func vapListSkipReason(resource string, err error) string {
	if apierrors.IsNotFound(err) {
		return fmt.Sprintf("the cluster does not serve admissionregistration.k8s.io/v1 %s (introduced in Kubernetes 1.30): %v", resource, err)
	}
	return fmt.Sprintf("%s list is not accessible: %v", resource, err)
}

// namedRulesToPodCreateCandidates adapts
// []admissionregistrationv1.NamedRuleWithOperations (the type used by
// MatchResources.ResourceRules/ExcludeResourceRules) to
// []admissionregistrationv1.RuleWithOperations so podCreateMatches, already
// written and tested for AdmissionWebhookVisibility, can be reused
// unchanged. A rule with a non-empty resourceNames allow-list is dropped
// rather than adapted: see the type's doc comment for why such a rule can
// never usefully match a controller-created pod.
func namedRulesToPodCreateCandidates(rules []admissionregistrationv1.NamedRuleWithOperations) []admissionregistrationv1.RuleWithOperations {
	var out []admissionregistrationv1.RuleWithOperations
	for _, r := range rules {
		if len(r.ResourceNames) > 0 {
			continue
		}
		out = append(out, r.RuleWithOperations)
	}
	return out
}

// matchResourcesMatchesPodExceptNamespace evaluates everything in a
// MatchResources object except namespaceSelector: exclusion rules,
// resourceRules (empty means "matches everything", matching.go:99-102),
// and objectSelector against the pod template's labels. namespaceSelector
// is evaluated separately by matchResourcesNamespaceMatches once this
// check knows whether a Namespace read is needed at all — the same
// two-phase split already used by AdmissionWebhookVisibility.
//
// A nil MatchResources means "no additional constraint": always true for
// an unset binding.matchResources (types.go: "If this is unset, all
// resources matched by the policy are validated by this binding").
func matchResourcesMatchesPodExceptNamespace(mr *admissionregistrationv1.MatchResources, podLabels map[string]string) (bool, error) {
	if mr == nil {
		return true, nil
	}
	if podCreateMatches(namedRulesToPodCreateCandidates(mr.ExcludeResourceRules)) {
		return false, nil
	}
	if len(mr.ResourceRules) > 0 && !podCreateMatches(namedRulesToPodCreateCandidates(mr.ResourceRules)) {
		return false, nil
	}
	return selectorMatchesOrEmpty(mr.ObjectSelector, podLabels)
}

// matchResourcesHasNamespaceSelector reports whether mr declares a
// non-empty namespaceSelector that would require reading the target
// Namespace's labels to evaluate.
func matchResourcesHasNamespaceSelector(mr *admissionregistrationv1.MatchResources) bool {
	return mr != nil && !isEmptySelector(mr.NamespaceSelector)
}

// matchResourcesNamespaceMatches evaluates only mr's namespaceSelector, the
// half of matching deliberately deferred by
// matchResourcesMatchesPodExceptNamespace.
func matchResourcesNamespaceMatches(mr *admissionregistrationv1.MatchResources, namespaceLabels map[string]string) (bool, error) {
	if mr == nil {
		return true, nil
	}
	return selectorMatchesOrEmpty(mr.NamespaceSelector, namespaceLabels)
}

func containsValidationAction(actions []admissionregistrationv1.ValidationAction, want admissionregistrationv1.ValidationAction) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

// validationMessages collects up to limit operator-authored Validation
// messages, skipping any validation with no message rather than
// fabricating one from the CEL expression itself.
func validationMessages(validations []admissionregistrationv1.Validation, limit int) []string {
	var out []string
	for _, v := range validations {
		if v.Message == "" {
			continue
		}
		out = append(out, v.Message)
		if len(out) == limit {
			break
		}
	}
	return out
}

func vapFinding(policy admissionregistrationv1.ValidatingAdmissionPolicy, binding admissionregistrationv1.ValidatingAdmissionPolicyBinding, workloadRef, describeWorkloadCmd string) model.Finding {
	resource := model.ResourceRef{Kind: "ValidatingAdmissionPolicy", Name: policy.Name}
	commands := []string{
		fmt.Sprintf("kubectl get validatingadmissionpolicy %s -o yaml", policy.Name),
		fmt.Sprintf("kubectl get validatingadmissionpolicybinding %s -o yaml", binding.Name),
		describeWorkloadCmd,
	}

	failurePolicy := effectiveFailurePolicy(policy.Spec.FailurePolicy)
	evidence := []string{
		fmt.Sprintf("policy=%s binding=%s failurePolicy=%s validationActions=%v validations=%d",
			policy.Name, binding.Name, failurePolicy, binding.Spec.ValidationActions, len(policy.Spec.Validations)),
	}
	if messages := validationMessages(policy.Spec.Validations, 3); len(messages) > 0 {
		evidence = append(evidence, fmt.Sprintf("validationMessages=%s", strings.Join(messages, " | ")))
	}

	if policy.Spec.ParamKind != nil && binding.Spec.ParamRef == nil && failurePolicy == admissionregistrationv1.Fail {
		evidence = append(evidence, fmt.Sprintf("paramKind=%s/%s", policy.Spec.ParamKind.APIVersion, policy.Spec.ParamKind.Kind))
		return model.Finding{
			CheckID:  ValidatingAdmissionPolicyVisibilityCheckID,
			Severity: model.SeverityMedium,
			Cause: fmt.Sprintf(
				"ValidatingAdmissionPolicy %q declares paramKind %s/%s but binding %q sets no paramRef: params will be null for every evaluation, and any validation expression that dereferences a field of params raises a CEL runtime error, which failurePolicy=Fail turns into a denial for %s's pod creation; this tool does not execute CEL and cannot say whether any of the %d validation(s) actually dereference params",
				policy.Name, policy.Spec.ParamKind.APIVersion, policy.Spec.ParamKind.Kind, binding.Name, workloadRef, len(policy.Spec.Validations),
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("set spec.paramRef on binding %q (or remove paramKind from policy %q if params were never intended), or set failurePolicy to Ignore if a CEL error here is safe to skip — which is correct depends on what this policy enforces", binding.Name, policy.Name),
				Commands:         commands,
				ContextDependent: true,
			},
			Resource: resource,
		}
	}

	return model.Finding{
		CheckID:  ValidatingAdmissionPolicyVisibilityCheckID,
		Severity: model.SeverityLow,
		Cause: fmt.Sprintf(
			"ValidatingAdmissionPolicy %q's CEL validations run on every pod %s creates via binding %q (validationActions include Deny): a failing validation denies pod creation, invisible at `kubectl apply` time of the workload and visible only as a FailedCreate event on the pod-creation source; this tool does not execute CEL and cannot say whether the current pod template would pass",
			policy.Name, workloadRef, binding.Name,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("review ValidatingAdmissionPolicy %q's validations and binding %q's validationActions if a pod creation failure for %s is ever attributed to admission", policy.Name, binding.Name, workloadRef),
			Commands:         commands,
			ContextDependent: true,
		},
		Resource: resource,
	}
}
