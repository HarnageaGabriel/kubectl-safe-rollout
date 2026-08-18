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

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	watchtools "k8s.io/client-go/tools/watch"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// rolloutStabilityWindow is how long RolloutComplete() must remain true
// continuously before Watch declares success. Without this window, a
// container without a readinessProbe counts as "Ready" at the exact moment
// the kubelet makes it Running (even before a possible crash brings it
// down): a pod that starts and crashes immediately afterward can produce a
// purely transient instant of AvailableReplicas>=UpdatedReplicas. Without
// waiting, Watch would report success for a rollout that is about to enter
// CrashLoopBackOff a moment later (observed in the e2e scenarios under
// test/e2e). 5s is a compromise: enough for an immediate crash or a
// liveness probe with a low initialDelaySeconds to emerge, but not long
// enough to make `watch` noticeably slow for a genuinely healthy rollout.
const rolloutStabilityWindow = 5 * time.Second

// restartedRolloutStabilityWindow is the quiet period required after Watch
// observes a restart count increase. The kubelet's own restart backoff grows
// beyond rolloutStabilityWindow: the liveness-probe e2e measurement held
// restartCount=3 from t=25s through t=49s, then reached 4 at t=54s. Requiring
// 30s prevents one of those backoff gaps from looking like a healthy rollout.
const restartedRolloutStabilityWindow = 30 * time.Second

// undeterminedGraceWindow is how long to wait before finalizing an entirely
// "-undetermined" outcome. Discovered in the ImagePullBackOff e2e scenarios:
// the kubelet posts Waiting=ErrImagePull on the Pod at the same instant
// (sometimes before) the Event with Reason=Failed and the detailed pull
// message becomes visible through List. If Watch stopped on the first tick,
// it would report "undetermined" even when evidence for a specific cause is
// about to arrive a moment later—exactly the false "I don't know" that the
// project's determinism constraint seeks to avoid as much as false
// certainty. A determined cause always takes immediate precedence: the
// window applies only when EVERY Finding in the tick is Undetermined.
const undeterminedGraceWindow = 3 * time.Second

// gracePollInterval is how often to reread Deployment/ReplicaSet/Event
// during undeterminedGraceWindow. It is the only declared exception to this
// file's "push-driven, not interval-based" principle (see
// docs/watch-vs-polling.md): narrow in scope (active only while a cause is
// ambiguous) and time-limited (never beyond undeterminedGraceWindow), not a
// drift toward polling as the primary mechanism.
const gracePollInterval = 500 * time.Millisecond

// WatchTarget groups what is needed to observe a rollout: the workload to
// observe and the clients used to read its state. It is distinct from
// Target, which represents state already read at a single instant:
// WatchTarget lives for the entire observation, while a fresh Target is
// rebuilt at every reevaluation (see evaluate).
type WatchTarget struct {
	Namespace string
	Workload  workload.Workload
	Client    kubernetes.Interface
	// LogTailer may be nil: no additional log evidence in
	// CrashLoopBackOff diagnoses.
	LogTailer LogTailer
	// StabilityWindow overrides both rolloutStabilityWindow and
	// restartedRolloutStabilityWindow when >0. It exists for tests (the
	// production windows would make the suite unnecessarily slow):
	// `cmd/kubectl-safe_rollout` never sets it, so real use always gets the
	// production defaults.
	StabilityWindow time.Duration
	// UndeterminedGraceWindow overrides undeterminedGraceWindow when >0,
	// for the same reason as StabilityWindow.
	UndeterminedGraceWindow time.Duration
}

// Outcome is the result of a completed observation: either the rollout
// succeeded, or it stopped with at least one diagnosed Finding. Results
// preserves grouping by Diagnoser (not a flattened []model.Finding), so
// the caller can render it with the same output.Group used by `check`,
// including SKIPs.
type Outcome struct {
	Succeeded bool
	Results   []Result
}

// Watch observes workload pods through the Watch API (client-go/tools/
// watch.RetryWatcher, with automatic re-list on reconnection) until the
// rollout succeeds or a diagnosable cause of stall/failure emerges. The
// reasons for choosing the Watch API over polling are documented in
// docs/watch-vs-polling.md, including the stated tradeoff of rereading
// Events and ReplicaSets on each tick instead of watching separate streams.
func Watch(ctx context.Context, wt WatchTarget) (Outcome, error) {
	podSelector, err := wt.Workload.PodSelector()
	if err != nil {
		return Outcome{}, fmt.Errorf("invalid pod selector for %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}
	selector := podSelector.String()
	restarts := newRestartObservations()
	for {
		outcome, relist, err := watchFromCurrentState(ctx, wt, selector, restarts)
		if relist {
			continue
		}
		return outcome, err
	}
}

// watchFromCurrentState performs a consistent List, evaluates the snapshot,
// then watches from that resourceVersion. relist=true indicates that the RV
// expired (HTTP 410): the caller starts again from a new List instead of
// losing events or pretending the stream is still reliable.
func watchFromCurrentState(ctx context.Context, wt WatchTarget, selector string, restarts *restartObservations) (Outcome, bool, error) {
	initial, err := wt.Client.CoreV1().Pods(wt.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return Outcome{}, false, fmt.Errorf("initial pod list for %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}
	pods := indexPods(initial.Items)

	cleanWindow := rolloutStabilityWindow
	restartedWindow := restartedRolloutStabilityWindow
	if wt.StabilityWindow > 0 {
		cleanWindow = wt.StabilityWindow
		restartedWindow = wt.StabilityWindow
	}
	graceWindow := wt.UndeterminedGraceWindow
	if graceWindow <= 0 {
		graceWindow = undeterminedGraceWindow
	}

	var stability *time.Timer
	var stabilityCh <-chan time.Time
	var stabilityRestartCounts containerRestartCounts
	// graceTicker repeats evaluateTick during the grace window instead of
	// waiting passively: a Pod stuck in ImagePullBackOff may produce no new
	// watch event for several seconds (the next pull attempt follows the
	// kubelet backoff), but the Event with Reason=Failed and the detailed
	// message is often already available through List a moment after the
	// first observed Waiting state. Without rereading, the grace window
	// would expire with the evidence from the first tick, exactly the
	// problem it was meant to solve (observed in the ImagePull e2e scenario).
	var graceTicker *time.Ticker
	var graceTickCh <-chan time.Time
	var graceDeadline time.Time
	var pendingUndetermined Outcome
	defer func() {
		if stability != nil {
			stability.Stop()
		}
		if graceTicker != nil {
			graceTicker.Stop()
		}
	}()
	stopGrace := func() {
		if graceTicker != nil {
			graceTicker.Stop()
			graceTicker = nil
			graceTickCh = nil
		}
		graceDeadline = time.Time{}
	}
	handleTick := func() (Outcome, bool, error) {
		restarts.observe(pods)
		tr, err := evaluateTick(ctx, wt, pods)
		if err != nil {
			return Outcome{}, true, err
		}
		switch {
		case tr.failureFound && allUndetermined(tr.outcome):
			pendingUndetermined = tr.outcome
			switch {
			case graceTicker == nil:
				graceDeadline = time.Now().Add(graceWindow)
				graceTicker = time.NewTicker(gracePollInterval)
				graceTickCh = graceTicker.C
			case time.Now().After(graceDeadline):
				// The window expired and every tick reread in the meantime
				// remains undetermined: this is the honest outcome.
				stopGrace()
				return pendingUndetermined, true, nil
			}
		case tr.failureFound:
			// At least one Finding is determined: it is the most specific
			// possible signal and always takes immediate precedence.
			stopGrace()
			return tr.outcome, true, nil
		default:
			stopGrace()
		}
		// RolloutComplete can remain true while a liveness probe repeatedly
		// kills a container. The pod may become Ready again during the
		// kubelet's restart backoff, so an increased restart count invalidates
		// the current quiet period even when the Deployment still looks
		// complete. Counts are relative to the window opening: preexisting
		// restart history must not make success impossible.
		restartedDuringWindow := stability != nil && stabilityRestartCounts.increased(pods)
		if restartedDuringWindow {
			stability.Stop()
			stability = nil
			stabilityCh = nil
			stabilityRestartCounts = nil
		}
		switch {
		case tr.complete && stability == nil:
			window := cleanWindow
			if restarts.increaseObserved {
				window = restartedWindow
			}
			stabilityRestartCounts = collectContainerRestartCounts(pods)
			stability = time.NewTimer(window)
			stabilityCh = stability.C
		case !tr.complete && stability != nil:
			stability.Stop()
			stability = nil
			stabilityCh = nil
			stabilityRestartCounts = nil
		}
		return Outcome{}, false, nil
	}

	if outcome, done, err := handleTick(); done {
		return outcome, false, err
	}
	deployment, err := wt.Client.AppsV1().Deployments(wt.Namespace).Get(ctx, wt.Workload.Name(), metav1.GetOptions{})
	if err != nil {
		return Outcome{}, false, fmt.Errorf("initial read of Deployment/%s for watch: %w", wt.Workload.Name(), err)
	}

	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			options.LabelSelector = selector
			return wt.Client.CoreV1().Pods(wt.Namespace).List(ctx, options)
		},
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			options.LabelSelector = selector
			return wt.Client.CoreV1().Pods(wt.Namespace).Watch(ctx, options)
		},
	}
	rw, err := watchtools.NewRetryWatcherWithContext(ctx, initial.ResourceVersion, lw)
	if err != nil {
		return Outcome{}, false, fmt.Errorf("starting pod watch for %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}
	defer rw.Stop()

	deploymentLW := &cache.ListWatch{
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", wt.Workload.Name()).String()
			return wt.Client.AppsV1().Deployments(wt.Namespace).Watch(ctx, options)
		},
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", wt.Workload.Name()).String()
			return wt.Client.AppsV1().Deployments(wt.Namespace).List(ctx, options)
		},
	}
	deploymentRW, err := watchtools.NewRetryWatcherWithContext(ctx, deployment.ResourceVersion, deploymentLW)
	if err != nil {
		return Outcome{}, false, fmt.Errorf("starting watch on Deployment/%s: %w", wt.Workload.Name(), err)
	}
	defer deploymentRW.Stop()

	for {
		select {
		case <-ctx.Done():
			return Outcome{}, false, ctx.Err()
		case <-stabilityCh:
			// The stability window expired without a diagnosable failure
			// emerging or RolloutComplete() becoming false in the meantime:
			// the success is real, not a transient instant. See
			// rolloutStabilityWindow.
			return Outcome{Succeeded: true}, false, nil
		case <-graceTickCh:
			// Check again while the cause remains ambiguous: see the
			// graceTicker comment above. handleTick decides whether the
			// window has expired.
			if outcome, done, err := handleTick(); done {
				return outcome, false, err
			}
		case ev, ok := <-rw.ResultChan():
			if relist, err := checkWatchEvent(ctx, ev, ok, "pod", wt.Workload.Name()); relist || err != nil {
				return Outcome{}, relist, err
			}
			applyPodEvent(pods, ev)
			if outcome, done, err := handleTick(); done {
				return outcome, false, err
			}
		case ev, ok := <-deploymentRW.ResultChan():
			if relist, err := checkWatchEvent(ctx, ev, ok, "Deployment", wt.Workload.Name()); relist || err != nil {
				return Outcome{}, relist, err
			}
			if outcome, done, err := handleTick(); done {
				return outcome, false, err
			}
		}
	}
}

// allUndetermined reports whether every Finding in the Outcome is
// Undetermined. An Outcome without any Finding does not count as "all
// determined": it should never happen (this is called only when
// tr.failureFound is true), but returning false in that case would still
// prevent finalizing an empty outcome by mistake.
func allUndetermined(o Outcome) bool {
	any := false
	for _, res := range o.Results {
		for _, f := range res.Findings {
			any = true
			if !f.Undetermined {
				return false
			}
		}
	}
	return any
}

func checkWatchEvent(ctx context.Context, event watch.Event, ok bool, resource, workloadName string) (bool, error) {
	if !ok {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("%s watch stream for Deployment/%s closed unexpectedly", resource, workloadName)
	}
	if event.Type != watch.Error {
		return false, nil
	}
	watchErr := apierrors.FromObject(event.Object)
	if apierrors.IsResourceExpired(watchErr) || apierrors.IsGone(watchErr) {
		return true, nil
	}
	return false, fmt.Errorf("watch %s per Deployment/%s: %w", resource, workloadName, watchErr)
}

func indexPods(items []corev1.Pod) map[types.UID]corev1.Pod {
	m := make(map[types.UID]corev1.Pod, len(items))
	for _, p := range items {
		m[p.UID] = p
	}
	return m
}

func applyPodEvent(pods map[types.UID]corev1.Pod, ev watch.Event) {
	pod, ok := ev.Object.(*corev1.Pod)
	if !ok {
		return
	}
	if ev.Type == watch.Deleted {
		delete(pods, pod.UID)
		return
	}
	pods[pod.UID] = *pod
}

func podSlice(pods map[types.UID]corev1.Pod) []corev1.Pod {
	out := make([]corev1.Pod, 0, len(pods))
	for _, p := range pods {
		out = append(out, p)
	}
	return out
}

type containerRestartKey struct {
	pod       types.UID
	podName   string
	container string
	init      bool
}

type containerRestartCounts map[containerRestartKey]int32

func collectContainerRestartCounts(pods map[types.UID]corev1.Pod) containerRestartCounts {
	counts := make(containerRestartCounts)
	for _, pod := range pods {
		for _, cs := range pod.Status.ContainerStatuses {
			counts[containerRestartKey{pod: pod.UID, podName: pod.Name, container: cs.Name}] = cs.RestartCount
		}
		for _, cs := range pod.Status.InitContainerStatuses {
			counts[containerRestartKey{pod: pod.UID, podName: pod.Name, container: cs.Name, init: true}] = cs.RestartCount
		}
	}
	return counts
}

func (baseline containerRestartCounts) increased(pods map[types.UID]corev1.Pod) bool {
	for key, count := range collectContainerRestartCounts(pods) {
		if previous, ok := baseline[key]; ok && count > previous {
			return true
		}
	}
	return false
}

type restartObservations struct {
	counts           containerRestartCounts
	increaseObserved bool
}

func newRestartObservations() *restartObservations {
	return &restartObservations{counts: make(containerRestartCounts)}
}

func (observations *restartObservations) observe(pods map[types.UID]corev1.Pod) {
	current := collectContainerRestartCounts(pods)
	for key, count := range current {
		if previous, ok := observations.counts[key]; ok && count > previous {
			observations.increaseObserved = true
		}
		observations.counts[key] = count
	}
}

// tick is the outcome of one reevaluation of the observed state. complete
// and failureFound are never both true: a diagnosed cause always takes
// priority (see evaluateTick), otherwise the distinction between "complete"
// and "not yet" would lose meaning.
type tick struct {
	complete     bool
	failureFound bool
	outcome      Outcome
}

// evaluateTick rereads workload state (Deployment, ReplicaSet, Event),
// rebuilds a fresh Target, then classifies the current tick. It does not
// decide on its own whether this is enough to stop observation:
// RolloutComplete()==true must remain stable for rolloutStabilityWindow
// (see handleTick in watchFromCurrentState) before becoming a definitive
// success. An error here is always fatal to observation (there is no useful
// way to continue if workload state can no longer be read), unlike narrow
// read errors inside individual Diagnosers, which degrade instead of
// propagating.
func evaluateTick(ctx context.Context, wt WatchTarget, pods map[types.UID]corev1.Pod) (tick, error) {
	d, err := wt.Client.AppsV1().Deployments(wt.Namespace).Get(ctx, wt.Workload.Name(), metav1.GetOptions{})
	if err != nil {
		return tick{}, fmt.Errorf("reading %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}
	wl := workload.FromDeployment(d)

	var collectionResults []Result
	replicaSets, err := listOwnedReplicaSets(ctx, wt, wl)
	if err != nil {
		collectionResults = append(collectionResults, SkipResult(QuotaDiagnoserID, err.Error()))
		replicaSets = nil
	}
	events, err := wt.Client.CoreV1().Events(wt.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		collectionResults = append(collectionResults, SkipResult("events", fmt.Sprintf("cannot list events in namespace %s: %v", wt.Namespace, err)))
		events = &corev1.EventList{}
	}

	target := Target{
		Namespace:   wt.Namespace,
		Workload:    wl,
		Client:      wt.Client,
		Pods:        podSlice(pods),
		ReplicaSets: replicaSets,
		EventsByUID: GroupEventsByInvolvedObject(events.Items),
		LogTailer:   wt.LogTailer,
	}

	results, err := RunDiagnosis(ctx, target)
	if err != nil {
		return tick{}, err
	}
	if len(collectionResults) > 0 {
		results = replaceSkippedResults(results, collectionResults)
	}
	// A diagnosed cause always takes precedence over RolloutComplete():
	// they are the same kind of transient signal that justifies the
	// stability window (a pod may appear Available momentarily and already
	// show CrashLoopBackOff in the same tick), and the concrete cause is
	// more useful than silence.
	if AnyFindings(results) {
		return tick{failureFound: true, outcome: Outcome{Results: results}}, nil
	}
	return tick{complete: wl.RolloutComplete()}, nil
}

func replaceSkippedResults(results, skipped []Result) []Result {
	byID := make(map[string]Result, len(skipped))
	for _, result := range skipped {
		byID[result.DiagnoserID] = result
	}
	out := make([]Result, 0, len(results)+len(skipped))
	for _, result := range results {
		if replacement, ok := byID[result.DiagnoserID]; ok {
			out = append(out, replacement)
			delete(byID, result.DiagnoserID)
			continue
		}
		out = append(out, result)
	}
	for _, result := range skipped {
		if _, ok := byID[result.DiagnoserID]; ok {
			out = append(out, result)
			delete(byID, result.DiagnoserID)
		}
	}
	return out
}

// listOwnedReplicaSets filters by OwnerReference instead of trusting only
// the label selector: a ReplicaSet from a previous Deployment revision may
// share the same labels as the current pod template (Kubernetes reuses them
// when the template does not change labels), but only the OwnerReference
// reliably identifies "owned by this Deployment".
func listOwnedReplicaSets(ctx context.Context, wt WatchTarget, wl workload.Workload) ([]appsv1.ReplicaSet, error) {
	podSelector, err := wl.PodSelector()
	if err != nil {
		return nil, fmt.Errorf("invalid pod selector for %s/%s: %w", wl.Kind(), wl.Name(), err)
	}
	selector := podSelector.String()
	list, err := wt.Client.AppsV1().ReplicaSets(wt.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing ReplicaSets for %s/%s: %w", wt.Namespace, wl.Name(), err)
	}
	owned := make([]appsv1.ReplicaSet, 0, len(list.Items))
	for _, rs := range list.Items {
		for _, ref := range rs.OwnerReferences {
			if ref.UID == wl.UID() {
				owned = append(owned, rs)
				break
			}
		}
	}
	return owned, nil
}
