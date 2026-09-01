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

// gracePollInterval is how often to reread the controller object/pod-creation
// source/Event during undeterminedGraceWindow. It is the only declared exception to this
// file's "push-driven, not interval-based" principle (see
// docs/watch-vs-polling.md): narrow in scope (active only while a cause is
// ambiguous) and time-limited (never beyond undeterminedGraceWindow), not a
// drift toward polling as the primary mechanism.
const gracePollInterval = 500 * time.Millisecond

// pausedSettleWindow is how long to keep checking for other diagnosable
// problems after a tick finds only that the rollout is paused, when no
// container has restarted yet. Discovered on kind: pausing an
// already-crashlooping Deployment made `watch` report rollout-paused alone,
// every time. Paused needs no pod symptom to fire (it reads spec.paused on
// the very first tick), while CrashLoopBackOff's Waiting state is only
// visible for part of the kubelet's restart cycle, so the first tick almost
// never lands on it. A paused workload is a stable, non-evolving state —
// nothing about it changes while it stays paused — so spending a short
// window double-checking for a pre-existing problem costs nothing and
// risks nothing, unlike widening the general "a determined cause always
// takes immediate precedence" rule for an in-flight rollout that is still
// genuinely changing.
//
// When a container has already restarted at least once, handleTick uses
// restartedWindow instead (see restartedRolloutStabilityWindow): measured
// on kind, a container at restartCount=4-5 went roughly 90 seconds between
// Pod updates, far past this window, because the kubelet's own backoff
// between attempts keeps growing. Restart evidence is exactly the signal
// that predicts this window will not be enough.
const pausedSettleWindow = 5 * time.Second

// pausedSettlePollInterval mirrors gracePollInterval for the same reason:
// the condition being waited for (an unrelated diagnoser also becoming
// determined) produces no watch event of its own, so periodic rereads are
// the only way to catch it within pausedSettleWindow.
const pausedSettlePollInterval = 500 * time.Millisecond

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
	// PausedSettleWindow overrides pausedSettleWindow when >0, for the same
	// reason as StabilityWindow.
	PausedSettleWindow time.Duration
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

// controllerObserver abstracts the differences between the two controller
// kinds the watch loop supports, Deployment and StatefulSet, so
// watchFromCurrentState/evaluateTick do not hardcode API calls for one kind.
// Built for exactly these two; do not add speculative hooks for kinds this
// project does not support yet (see workload.Workload's own doc comment).
type controllerObserver interface {
	// get reads the live controller object and returns it wrapped as a
	// fresh workload.Workload plus its current ResourceVersion. Called once
	// before starting the RetryWatcher (for its initial resourceVersion) and
	// once per tick from evaluateTick. A read error here is always fatal to
	// observation, matching evaluateTick's existing contract: there is no
	// useful way to continue if the controller object can no longer be
	// read.
	get(ctx context.Context, namespace, name string) (wl workload.Workload, resourceVersion string, err error)
	// listWatch builds the ListWatch used to start the RetryWatcher on the
	// controller object itself (Pods are handled separately, identically
	// for both kinds).
	listWatch(namespace, name string) *cache.ListWatch
	// podCreationSources returns this tick's PodCreationSource entries (see
	// PodCreationSource). A read error here degrades to Result.Skipped on
	// the Quota Diagnoser in evaluateTick, matching the existing behavior
	// for listOwnedReplicaSets: a narrow read error must not abort the
	// whole tick.
	podCreationSources(ctx context.Context, wl workload.Workload) ([]PodCreationSource, error)
}

// newControllerObserver selects the controllerObserver implementation for
// kind, mirroring the switch-on-kind idiom already used by
// kube.ResolveWorkload for the same two supported kinds.
func newControllerObserver(client kubernetes.Interface, kind string) (controllerObserver, error) {
	switch kind {
	case "Deployment":
		return deploymentObserver{client: client}, nil
	case "StatefulSet":
		return statefulSetObserver{client: client}, nil
	default:
		return nil, fmt.Errorf("watch does not support kind %q", kind)
	}
}

type deploymentObserver struct {
	client kubernetes.Interface
}

func (o deploymentObserver) get(ctx context.Context, namespace, name string) (workload.Workload, string, error) {
	d, err := o.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	return workload.FromDeployment(d), d.ResourceVersion, nil
}

func (o deploymentObserver) listWatch(namespace, name string) *cache.ListWatch {
	return &cache.ListWatch{
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", name).String()
			return o.client.AppsV1().Deployments(namespace).Watch(ctx, options)
		},
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", name).String()
			return o.client.AppsV1().Deployments(namespace).List(ctx, options)
		},
	}
}

// podCreationSources wraps the Deployment's owned ReplicaSets (see
// listOwnedReplicaSets): the Deployment controller never creates Pods
// directly, so pod-creation rejections surface as events on the ReplicaSet.
func (o deploymentObserver) podCreationSources(ctx context.Context, wl workload.Workload) ([]PodCreationSource, error) {
	replicaSets, err := listOwnedReplicaSets(ctx, o.client, wl.Namespace(), wl)
	if err != nil {
		return nil, err
	}
	sources := make([]PodCreationSource, 0, len(replicaSets))
	for _, rs := range replicaSets {
		sources = append(sources, PodCreationSource{Kind: "ReplicaSet", Namespace: rs.Namespace, Name: rs.Name, UID: rs.UID})
	}
	return sources, nil
}

type statefulSetObserver struct {
	client kubernetes.Interface
}

func (o statefulSetObserver) get(ctx context.Context, namespace, name string) (workload.Workload, string, error) {
	s, err := o.client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, "", err
	}
	return workload.FromStatefulSet(s), s.ResourceVersion, nil
}

func (o statefulSetObserver) listWatch(namespace, name string) *cache.ListWatch {
	return &cache.ListWatch{
		WatchFuncWithContext: func(ctx context.Context, options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", name).String()
			return o.client.AppsV1().StatefulSets(namespace).Watch(ctx, options)
		},
		ListWithContextFunc: func(ctx context.Context, options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = fields.OneTermEqualSelector("metadata.name", name).String()
			return o.client.AppsV1().StatefulSets(namespace).List(ctx, options)
		},
	}
}

// podCreationSources returns a single entry that IS the StatefulSet itself:
// its controller creates Pods directly (no intermediate ReplicaSet), so its
// own FailedCreate events already carry the evidence Quota needs. No API
// call needed: wl already reflects the object read this tick by get.
func (o statefulSetObserver) podCreationSources(_ context.Context, wl workload.Workload) ([]PodCreationSource, error) {
	return []PodCreationSource{{Kind: "StatefulSet", Namespace: wl.Namespace(), Name: wl.Name(), UID: wl.UID()}}, nil
}

// Watch observes workload pods through the Watch API (client-go/tools/
// watch.RetryWatcher, with automatic re-list on reconnection) until the
// rollout succeeds or a diagnosable cause of stall/failure emerges. The
// reasons for choosing the Watch API over polling are documented in
// docs/watch-vs-polling.md, including the stated tradeoff of rereading
// Events and pod-creation sources on each tick instead of watching separate
// streams.
func Watch(ctx context.Context, wt WatchTarget) (Outcome, error) {
	podSelector, err := wt.Workload.PodSelector()
	if err != nil {
		return Outcome{}, fmt.Errorf("invalid pod selector for %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}
	selector := podSelector.String()
	observer, err := newControllerObserver(wt.Client, wt.Workload.Kind())
	if err != nil {
		return Outcome{}, err
	}
	restarts := newRestartObservations()
	for {
		outcome, relist, err := watchFromCurrentState(ctx, wt, selector, observer, restarts)
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
func watchFromCurrentState(ctx context.Context, wt WatchTarget, selector string, observer controllerObserver, restarts *restartObservations) (Outcome, bool, error) {
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
	pausedWindow := wt.PausedSettleWindow
	if pausedWindow <= 0 {
		pausedWindow = pausedSettleWindow
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
	// pausedTicker mirrors graceTicker's mechanism (see pausedSettleWindow)
	// but for a different predicate: the outcome is entirely explained by
	// Paused, and this tick is given a bounded chance to also observe an
	// unrelated diagnoser becoming determined before finalizing.
	var pausedTicker *time.Ticker
	var pausedTickCh <-chan time.Time
	var pausedDeadline time.Time
	var pendingPausedOnly Outcome
	defer func() {
		if stability != nil {
			stability.Stop()
		}
		if graceTicker != nil {
			graceTicker.Stop()
		}
		if pausedTicker != nil {
			pausedTicker.Stop()
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
	stopPausedSettle := func() {
		if pausedTicker != nil {
			pausedTicker.Stop()
			pausedTicker = nil
			pausedTickCh = nil
		}
		pausedDeadline = time.Time{}
	}
	handleTick := func() (Outcome, bool, error) {
		restarts.observe(pods)
		tr, err := evaluateTick(ctx, wt, observer, pods)
		if err != nil {
			return Outcome{}, true, err
		}
		switch {
		case tr.failureFound && allUndetermined(tr.outcome):
			stopPausedSettle()
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
		case tr.failureFound && isSettleWindowOnly(tr.outcome):
			stopGrace()
			pendingPausedOnly = tr.outcome
			switch {
			case pausedTicker == nil:
				// A container that has already restarted at least once is
				// evidence that something was wrong before the pause, and the
				// kubelet's own restart backoff (see
				// restartedRolloutStabilityWindow) can keep the next
				// Waiting=CrashLoopBackOff transition dozens of seconds away.
				// Reuse that same, already-justified longer window instead of
				// inventing a second unrelated constant; with no restart
				// evidence yet, there is nothing pointing at a longer wait
				// being worth the latency.
				window := pausedWindow
				if anyRestartObserved(pods) {
					window = restartedWindow
				}
				pausedDeadline = time.Now().Add(window)
				pausedTicker = time.NewTicker(pausedSettlePollInterval)
				pausedTickCh = pausedTicker.C
			case time.Now().After(pausedDeadline):
				// The window expired with nothing else determined: paused is
				// genuinely the whole story, at least for now.
				stopPausedSettle()
				return pendingPausedOnly, true, nil
			}
		case tr.failureFound:
			// At least one Finding is determined and it is not explained by
			// Paused alone: it is the most specific possible signal (which
			// now includes rollout-paused too, if this tick still finds it,
			// since evaluateTick reruns every Diagnoser) and always takes
			// immediate precedence.
			stopGrace()
			stopPausedSettle()
			return tr.outcome, true, nil
		default:
			stopGrace()
			stopPausedSettle()
		}
		// RolloutComplete can remain true while a liveness probe repeatedly
		// kills a container. The pod may become Ready again during the
		// kubelet's restart backoff, so an increased restart count invalidates
		// the current quiet period even when the controller still looks
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
	_, controllerRV, err := observer.get(ctx, wt.Namespace, wt.Workload.Name())
	if err != nil {
		return Outcome{}, false, fmt.Errorf("initial read of %s/%s for watch: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
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

	controllerLW := observer.listWatch(wt.Namespace, wt.Workload.Name())
	controllerRW, err := watchtools.NewRetryWatcherWithContext(ctx, controllerRV, controllerLW)
	if err != nil {
		return Outcome{}, false, fmt.Errorf("starting watch on %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}
	defer controllerRW.Stop()

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
		case <-pausedTickCh:
			// Check again while rollout-paused is the only finding: see the
			// pausedSettleWindow comment above. handleTick decides whether
			// the window has expired.
			if outcome, done, err := handleTick(); done {
				return outcome, false, err
			}
		case ev, ok := <-rw.ResultChan():
			if relist, err := checkWatchEvent(ctx, ev, ok, "pod", wt.Workload.Kind(), wt.Workload.Name()); relist || err != nil {
				return Outcome{}, relist, err
			}
			applyPodEvent(pods, ev)
			if outcome, done, err := handleTick(); done {
				return outcome, false, err
			}
		case ev, ok := <-controllerRW.ResultChan():
			if relist, err := checkWatchEvent(ctx, ev, ok, wt.Workload.Kind(), wt.Workload.Kind(), wt.Workload.Name()); relist || err != nil {
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

// settleWindowDiagnoserIDs are the diagnosers whose Finding can fire from a
// spec field alone, with no pod-level symptom required at all: Paused reads
// spec.paused, and StatefulSetUpdate reads spec.updateStrategy plus
// Status.UpdateRevision/CurrentRevision. Each shares the exact risk that
// justified pausedSettleWindow in the first place (see its comment): on the
// very first tick, any of them can win over a pod-level cause (a preexisting
// crash loop, a stuck image pull) that simply has not surfaced in this
// tick's snapshot yet, because a spec-only signal needs no pod state to be
// visible while the pod-level cause might only be observable for part of
// the kubelet's own cycle.
var settleWindowDiagnoserIDs = map[string]bool{
	PausedDiagnoserID:            true,
	StatefulSetUpdateDiagnoserID: true,
}

// isSettleWindowOnly reports whether every Finding in the Outcome came from
// a Diagnoser in settleWindowDiagnoserIDs. Used to decide whether a tick's
// outcome is worth a second look (see pausedSettleWindow) before finalizing.
func isSettleWindowOnly(o Outcome) bool {
	any := false
	for _, res := range o.Results {
		for range res.Findings {
			any = true
			if !settleWindowDiagnoserIDs[res.DiagnoserID] {
				return false
			}
		}
	}
	return any
}

func checkWatchEvent(ctx context.Context, event watch.Event, ok bool, streamLabel, kind, workloadName string) (bool, error) {
	if !ok {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("%s watch stream for %s/%s closed unexpectedly", streamLabel, kind, workloadName)
	}
	if event.Type != watch.Error {
		return false, nil
	}
	watchErr := apierrors.FromObject(event.Object)
	if apierrors.IsResourceExpired(watchErr) || apierrors.IsGone(watchErr) {
		return true, nil
	}
	return false, fmt.Errorf("watch %s for %s/%s: %w", streamLabel, kind, workloadName, watchErr)
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

// anyRestartObserved reports whether any container in pods has already
// restarted at least once, regardless of when. Used to pick a longer
// pausedSettleWindow: prior restarts are evidence something was already
// wrong before the workload was paused.
func anyRestartObserved(pods map[types.UID]corev1.Pod) bool {
	for _, count := range collectContainerRestartCounts(pods) {
		if count > 0 {
			return true
		}
	}
	return false
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

// evaluateTick rereads workload state (the controller object, its
// pod-creation sources, Events), rebuilds a fresh Target, then classifies
// the current tick. It does not decide on its own whether this is enough to
// stop observation: RolloutComplete()==true must remain stable for
// rolloutStabilityWindow (see handleTick in watchFromCurrentState) before
// becoming a definitive success. An error here is always fatal to
// observation (there is no useful way to continue if workload state can no
// longer be read), unlike narrow read errors inside individual Diagnosers,
// which degrade instead of propagating.
func evaluateTick(ctx context.Context, wt WatchTarget, observer controllerObserver, pods map[types.UID]corev1.Pod) (tick, error) {
	wl, _, err := observer.get(ctx, wt.Namespace, wt.Workload.Name())
	if err != nil {
		return tick{}, fmt.Errorf("reading %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}

	var collectionResults []Result
	sources, err := observer.podCreationSources(ctx, wl)
	if err != nil {
		collectionResults = append(collectionResults, SkipResult(QuotaDiagnoserID, err.Error()))
		sources = nil
	}
	events, err := wt.Client.CoreV1().Events(wt.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		collectionResults = append(collectionResults, SkipResult("events", fmt.Sprintf("cannot list events in namespace %s: %v", wt.Namespace, err)))
		events = &corev1.EventList{}
	}

	target := Target{
		Namespace:          wt.Namespace,
		Workload:           wl,
		Client:             wt.Client,
		Pods:               podSlice(pods),
		PodCreationSources: sources,
		EventsByUID:        GroupEventsByInvolvedObject(events.Items),
		LogTailer:          wt.LogTailer,
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
func listOwnedReplicaSets(ctx context.Context, client kubernetes.Interface, namespace string, wl workload.Workload) ([]appsv1.ReplicaSet, error) {
	podSelector, err := wl.PodSelector()
	if err != nil {
		return nil, fmt.Errorf("invalid pod selector for %s/%s: %w", wl.Kind(), wl.Name(), err)
	}
	selector := podSelector.String()
	list, err := client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing ReplicaSets for %s/%s: %w", namespace, wl.Name(), err)
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
