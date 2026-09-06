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
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// AdmissionWebhookVisibilityCheckID is the stable identifier for this check.
const AdmissionWebhookVisibilityCheckID = "admission-webhook-visibility"

// AdmissionWebhookVisibility surfaces admission webhooks that sit in this
// workload's pod-creation path, using only facts the vendored API types
// guarantee, never a probe of the webhook's actual logic.
//
// Two facts from k8s.io/api/admissionregistration/v1/types.go anchor
// everything this check can and cannot claim:
//
//   - failurePolicy (ValidatingWebhook, lines 814-817; the identical field
//     exists on MutatingWebhook) governs ONLY errors calling the webhook
//     (backend down, timeout, TLS failure), never an explicit `allowed:
//     false` response from a healthy webhook. Defaults to Fail. This means
//     a High/Medium finding here is never a promise that "the rollout will
//     be blocked" in general — only that a specific, verified condition
//     (backend Service missing or backend not ready, with failurePolicy
//     effectively Fail) guarantees a call-error block. A webhook that is
//     perfectly reachable can still reject the pod for reasons only its
//     own logic knows, and this check says nothing about that case at all.
//   - matchConditions (lines 922-939) are CEL expressions this check does
//     not evaluate. A webhook that declares any is excluded from every
//     conclusion below: this check cannot tell whether it would actually
//     be invoked for this workload's pods.
//
// Verified live on kind (v1.37.0) in the same session that produced this
// check: a ValidatingWebhookConfiguration matching pods/CREATE in this
// namespace, failurePolicy Fail, pointed at a Service that does not exist,
// blocked every single pod creation — the ReplicaSet's FailedCreate event
// read verbatim:
//
//	Error creating: Internal error occurred: failed calling webhook
//	"fail.example.com": failed to call webhook: Post
//	"https://does-not-exist.wh-test.svc:443/validate?timeout=3s":
//	service "does-not-exist" not found
//
// The identical webhook with failurePolicy Ignore let the same rollout
// complete against the same unreachable backend — confirming failurePolicy
// alone decides whether a call error blocks, independent of anything about
// the backend. A backend Service that exists but has zero ready
// EndpointSlice addresses produced a different message ("dial tcp ...:
// connect: connection refused") but the identical zero-pods-created
// outcome; this check treats that case as Medium rather than High because
// an unready backend is often transient, while a Service that does not
// exist at all is a permanent configuration error.
//
// Findings, all Severity fixed by which fact was verified, not by
// judgment:
//
//   - High: a ValidatingWebhookConfiguration webhook matches this
//     workload's pod/CREATE admission, its effective failurePolicy is
//     Fail, and its clientConfig.service names a Service that does not
//     exist (apierrors.IsNotFound). A guaranteed call-error block.
//   - Medium: the same match and failurePolicy, but the Service exists
//     with zero ready EndpointSlice addresses. Still a guaranteed
//     call-error block today, scored lower because a restarting backend
//     is frequently transient.
//   - Low, mutating: any MutatingWebhookConfiguration webhook that
//     matches pod/CREATE, regardless of failurePolicy or backend
//     reachability. Purely informational: the pod Kubernetes actually
//     creates may differ from the template every other check in this run
//     reasons about.
//   - Low, validating: a ValidatingWebhookConfiguration webhook that
//     matches, with effective failurePolicy Fail, whose backend resolves
//     with ready endpoints. No blockage is guaranteed today; this is
//     inventory ("this webhook is in the path"), not a warning. Nothing is
//     reported when the effective failurePolicy is Ignore: a call error
//     there does not block, and this tool has no other verifiable fact
//     about the webhook's own logic to report.
//
// Scope, v1, stated explicitly:
//
//   - only rules matching the pods resource and the CREATE (or *)
//     operation are considered; a Deployment/StatefulSet UPDATE webhook is
//     out of scope; that failure path is already visible inline to the
//     operator (`kubectl apply` fails immediately), unlike a pod-creation
//     rejection buried inside a rollout.
//   - clientConfig.url (an external, non-in-cluster webhook) produces no
//     resolvable fact this tool can check without probing network
//     reachability, which this project never does: those webhooks are
//     silently excluded from every finding category, including the
//     purely informational mutating one.
//   - this check never evaluates the webhook's own decision logic: a
//     perfectly reachable webhook can still reject a pod for reasons only
//     its author knows.
//   - objectSelector is evaluated against the pod template as written
//     (Workload.PodLabels()); if an earlier webhook in the mutation chain
//     would change those labels first, this evaluation is only exact for
//     the first webhook actually invoked.
//   - matchPolicy Exact vs Equivalent is not modeled distinctly: pods are
//     always core/v1, so the distinction never changes which webhooks
//     apply to a Pod the way it can for a resource served under multiple
//     API groups/versions.
//   - ValidatingAdmissionPolicy/MutatingAdmissionPolicy (the newer,
//     CEL-native admission mechanism in the same API group) and the
//     pods/eviction and deployments/scale subresources are out of scope.
//   - sideEffects is not used: it only matters for dry-run, which this
//     tool never performs.
//   - timeoutSeconds is reported only as Evidence, never as a finding: no
//     specific value is a documented threshold of failure.
type AdmissionWebhookVisibility struct{}

// ID implements check.Check.
func (AdmissionWebhookVisibility) ID() string { return AdmissionWebhookVisibilityCheckID }

// Run implements check.Check.
func (c AdmissionWebhookVisibility) Run(ctx context.Context, target Target) (Result, error) {
	validatingList, err := target.Client.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("validatingwebhookconfiguration list is not accessible: %v", err)), nil
	}
	mutatingList, err := target.Client.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("mutatingwebhookconfiguration list is not accessible: %v", err)), nil
	}

	podLabels := target.Workload.PodLabels()

	type relevantValidating struct {
		configName string
		webhook    admissionregistrationv1.ValidatingWebhook
	}
	type relevantMutating struct {
		configName string
		webhook    admissionregistrationv1.MutatingWebhook
	}

	var candidateValidating []relevantValidating
	for _, cfg := range validatingList.Items {
		for _, wh := range cfg.Webhooks {
			if !podCreateMatches(wh.Rules) || len(wh.MatchConditions) > 0 {
				continue
			}
			if match, err := selectorMatchesOrEmpty(wh.ObjectSelector, podLabels); err != nil || !match {
				continue
			}
			candidateValidating = append(candidateValidating, relevantValidating{configName: cfg.Name, webhook: wh})
		}
	}
	var candidateMutating []relevantMutating
	for _, cfg := range mutatingList.Items {
		for _, wh := range cfg.Webhooks {
			if !podCreateMatches(wh.Rules) || len(wh.MatchConditions) > 0 {
				continue
			}
			if match, err := selectorMatchesOrEmpty(wh.ObjectSelector, podLabels); err != nil || !match {
				continue
			}
			candidateMutating = append(candidateMutating, relevantMutating{configName: cfg.Name, webhook: wh})
		}
	}

	// namespaceSelector defaults to the empty LabelSelector ("matches
	// everything"), which needs no Namespace read at all. Only fetch the
	// Namespace object when at least one otherwise-relevant webhook
	// actually declares a non-empty one: a namespace read failing must not
	// Skip this entire check when nothing here actually needs it.
	needsNamespace := false
	for _, rv := range candidateValidating {
		if !isEmptySelector(rv.webhook.NamespaceSelector) {
			needsNamespace = true
		}
	}
	for _, rm := range candidateMutating {
		if !isEmptySelector(rm.webhook.NamespaceSelector) {
			needsNamespace = true
		}
	}

	var namespaceLabels map[string]string
	if needsNamespace {
		ns, err := target.Client.CoreV1().Namespaces().Get(ctx, target.Namespace, metav1.GetOptions{})
		if err != nil {
			return Skip(c.ID(), fmt.Sprintf("namespace %q is not accessible, and at least one matching webhook has a non-empty namespaceSelector: %v", target.Namespace, err)), nil
		}
		namespaceLabels = ns.Labels
	}

	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())
	describeWorkloadCmd := fmt.Sprintf("kubectl describe %s %s -n %s", strings.ToLower(target.Workload.Kind()), target.Workload.Name(), target.Namespace)

	var findings []model.Finding
	for _, rv := range candidateValidating {
		if match, err := selectorMatchesOrEmpty(rv.webhook.NamespaceSelector, namespaceLabels); err != nil || !match {
			continue
		}
		if f, ok := validatingFinding(ctx, target, rv.configName, rv.webhook, workloadRef, describeWorkloadCmd); ok {
			findings = append(findings, f)
		}
	}
	for _, rm := range candidateMutating {
		if match, err := selectorMatchesOrEmpty(rm.webhook.NamespaceSelector, namespaceLabels); err != nil || !match {
			continue
		}
		if f, ok := mutatingFinding(rm.configName, rm.webhook, workloadRef, describeWorkloadCmd); ok {
			findings = append(findings, f)
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// podCreateMatches reports whether at least one rule matches a namespaced
// Pod CREATE (or *) request — the only shape of admission this check
// models (see the type's doc comment for the scope this deliberately
// excludes).
func podCreateMatches(rules []admissionregistrationv1.RuleWithOperations) bool {
	for _, r := range rules {
		if !containsAny(r.APIGroups, "", "*") {
			continue
		}
		if !containsAny(r.APIVersions, "v1", "*") {
			continue
		}
		if !containsAny(r.Resources, "pods", "*", "*/*") {
			continue
		}
		if !containsAny(operationStrings(r.Operations), "CREATE", "*") {
			continue
		}
		if !scopeMatchesNamespaced(r.Scope) {
			continue
		}
		return true
	}
	return false
}

func operationStrings(ops []admissionregistrationv1.OperationType) []string {
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = string(op)
	}
	return out
}

// scopeMatchesNamespaced reports whether a Rule's scope can match a Pod, an
// always-namespaced resource. nil defaults to "*" per the vendored doc
// comment on Rule.Scope.
func scopeMatchesNamespaced(scope *admissionregistrationv1.ScopeType) bool {
	if scope == nil {
		return true
	}
	switch *scope {
	case admissionregistrationv1.NamespacedScope, admissionregistrationv1.AllScopes:
		return true
	default:
		return false
	}
}

func containsAny(list []string, candidates ...string) bool {
	for _, s := range list {
		for _, c := range candidates {
			if s == c {
				return true
			}
		}
	}
	return false
}

// isEmptySelector reports whether sel is nil or has no match criteria at
// all, i.e. the documented default that matches everything without
// requiring the object it would otherwise be evaluated against.
func isEmptySelector(sel *metav1.LabelSelector) bool {
	return sel == nil || (len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0)
}

// selectorMatchesOrEmpty evaluates sel against set, treating a nil or empty
// selector as always matching (the documented default for both
// namespaceSelector and objectSelector). A malformed selector — which
// admission itself should already have rejected — is treated as
// non-matching rather than aborting evaluation of every other webhook.
func selectorMatchesOrEmpty(sel *metav1.LabelSelector, set map[string]string) (bool, error) {
	if isEmptySelector(sel) {
		return true, nil
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false, err
	}
	return s.Matches(labels.Set(set)), nil
}

// effectiveFailurePolicy applies the documented default (Fail) so a fake
// clientset fixture with a nil field — client-go/kubernetes/fake performs
// no apiserver-side defaulting — behaves exactly like a live cluster's
// defaulted object.
func effectiveFailurePolicy(fp *admissionregistrationv1.FailurePolicyType) admissionregistrationv1.FailurePolicyType {
	if fp != nil {
		return *fp
	}
	return admissionregistrationv1.Fail
}

// backendState is the verified fact this check can establish about a
// webhook's in-cluster Service backend, without ever probing real network
// reachability.
type backendState int

const (
	// backendUnknown means the Service or its EndpointSlices could not be
	// read; this is not proof of a problem, so no finding is produced.
	backendUnknown backendState = iota
	backendNotFound
	backendZeroReady
	backendReady
)

func resolveBackend(ctx context.Context, target Target, namespace, name string) backendState {
	_, err := target.Client.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return backendNotFound
	case err != nil:
		return backendUnknown
	}

	slices, err := target.Client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + name,
	})
	if err != nil {
		return backendUnknown
	}
	for _, slice := range slices.Items {
		for _, ep := range slice.Endpoints {
			// Ready == nil is defined by the API as "unknown", which
			// consumers are expected to treat as ready — the same
			// convention already applied in service.go.
			if (ep.Conditions.Ready == nil || *ep.Conditions.Ready) && len(ep.Addresses) > 0 {
				return backendReady
			}
		}
	}
	return backendZeroReady
}

func servicePort(svc *admissionregistrationv1.ServiceReference) int32 {
	if svc.Port != nil {
		return *svc.Port
	}
	return 443 // documented default, ServiceReference.Port doc comment.
}

func validatingFinding(ctx context.Context, target Target, configName string, wh admissionregistrationv1.ValidatingWebhook, workloadRef, describeWorkloadCmd string) (model.Finding, bool) {
	if effectiveFailurePolicy(wh.FailurePolicy) != admissionregistrationv1.Fail {
		// An error calling this webhook does not block admission: this
		// tool has no other verifiable fact about the webhook's own
		// decision logic to report.
		return model.Finding{}, false
	}
	svcRef := wh.ClientConfig.Service
	if svcRef == nil {
		// clientConfig.url: an external webhook, no resolvable fact.
		return model.Finding{}, false
	}

	resource := model.ResourceRef{Kind: "ValidatingWebhookConfiguration", Name: configName}
	evidenceBase := fmt.Sprintf("webhookConfiguration=%s webhook=%s failurePolicy=Fail clientConfig.service=%s/%s:%d",
		configName, wh.Name, svcRef.Namespace, svcRef.Name, servicePort(svcRef))
	commands := []string{
		fmt.Sprintf("kubectl get validatingwebhookconfiguration %s -o yaml", configName),
		fmt.Sprintf("kubectl get svc,endpointslice -n %s -l kubernetes.io/service-name=%s", svcRef.Namespace, svcRef.Name),
		describeWorkloadCmd,
	}

	switch resolveBackend(ctx, target, svcRef.Namespace, svcRef.Name) {
	case backendNotFound:
		return model.Finding{
			CheckID:  AdmissionWebhookVisibilityCheckID,
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"ValidatingWebhookConfiguration %q's webhook %q is in the admission path for %s's pod creation (failurePolicy=Fail) and its backend Service %q does not exist in namespace %q: every pod creation this webhook is called for will be rejected on the call error alone",
				configName, wh.Name, workloadRef, svcRef.Name, svcRef.Namespace,
			),
			Evidence: []string{evidenceBase},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("restore Service %q in namespace %q, point webhook %q's clientConfig at the correct Service, or set failurePolicy to Ignore if calls to this webhook are safe to skip on error — which is correct depends on what this webhook enforces", svcRef.Name, svcRef.Namespace, wh.Name),
				Commands:         commands,
				ContextDependent: true,
			},
			Resource: resource,
		}, true
	case backendZeroReady:
		return model.Finding{
			CheckID:  AdmissionWebhookVisibilityCheckID,
			Severity: model.SeverityMedium,
			Cause: fmt.Sprintf(
				"ValidatingWebhookConfiguration %q's webhook %q is in the admission path for %s's pod creation (failurePolicy=Fail) and its backend Service %q exists but has zero ready endpoints: pod creation this webhook is called for is rejected on the call error until the backend becomes ready",
				configName, wh.Name, workloadRef, svcRef.Name,
			),
			Evidence: []string{evidenceBase},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("investigate why webhook %q's backend Service %q has no ready endpoints (a restarting or misconfigured Deployment behind it are common causes); if it is expected to stay unready, set failurePolicy to Ignore", wh.Name, svcRef.Name),
				Commands:         commands,
				ContextDependent: true,
			},
			Resource: resource,
		}, true
	case backendReady:
		return model.Finding{
			CheckID:  AdmissionWebhookVisibilityCheckID,
			Severity: model.SeverityLow,
			Cause: fmt.Sprintf(
				"ValidatingWebhookConfiguration %q's webhook %q is in the admission path for %s's pod creation (failurePolicy=Fail) and its backend currently resolves with ready endpoints: no blockage is guaranteed today, but this webhook can still reject the pod for reasons only its own logic knows, which no other check in this run evaluates",
				configName, wh.Name, workloadRef,
			),
			Evidence: []string{evidenceBase},
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("informational: no action required unless webhook %q's own admission logic is suspected of rejecting this workload's pods", wh.Name),
				Commands:         []string{fmt.Sprintf("kubectl get validatingwebhookconfiguration %s -o yaml", configName)},
				ContextDependent: true,
			},
			Resource: resource,
		}, true
	default: // backendUnknown
		return model.Finding{}, false
	}
}

func mutatingFinding(configName string, wh admissionregistrationv1.MutatingWebhook, workloadRef, describeWorkloadCmd string) (model.Finding, bool) {
	if wh.ClientConfig.Service == nil {
		// clientConfig.url: an external webhook, no resolvable fact.
		return model.Finding{}, false
	}
	svcRef := wh.ClientConfig.Service
	return model.Finding{
		CheckID:  AdmissionWebhookVisibilityCheckID,
		Severity: model.SeverityLow,
		Cause: fmt.Sprintf(
			"MutatingWebhookConfiguration %q's webhook %q is in the admission path for %s's pod creation: the pod Kubernetes actually creates may differ from this pod template, while every other check in this run reasons about the template as written",
			configName, wh.Name, workloadRef,
		),
		Evidence: []string{fmt.Sprintf("webhookConfiguration=%s webhook=%s clientConfig.service=%s/%s:%d",
			configName, wh.Name, svcRef.Namespace, svcRef.Name, servicePort(svcRef))},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("review webhook %q's mutation logic if any other check's conclusion depends on an exact container image, resource value, or other field it might change", wh.Name),
			Commands:         []string{fmt.Sprintf("kubectl get mutatingwebhookconfiguration %s -o yaml", configName), describeWorkloadCmd},
			ContextDependent: true,
		},
		Resource: model.ResourceRef{Kind: "MutatingWebhookConfiguration", Name: configName},
	}, true
}
