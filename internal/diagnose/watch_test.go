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

package diagnose_test

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// withListResourceVersion adds a reactor that assigns a non-empty
// resourceVersion to the pod List. This works around the fake clientset,
// not a production code requirement: against a real apiserver, a List
// always carries a valid resourceVersion in its ListMeta, but the fake
// clientset's ObjectTracker never sets it (see
// k8s.io/client-go/testing/fixture.go, tracker.List).
// watchtools.NewRetryWatcherWithContext explicitly rejects an empty RV
// (or "0"): without this reactor, Watch() could never start the stream
// against a fake clientset, regardless of whether the production code is
// correct.
func withListResourceVersion(client *fake.Clientset, rv string) {
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj, err := client.Tracker().List(action.GetResource(), corev1.SchemeGroupVersion.WithKind("Pod"), action.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		list, ok := obj.(*corev1.PodList)
		if !ok {
			return true, obj, nil
		}
		list.ResourceVersion = rv
		return true, list, nil
	})
}

func watchDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace, Generation: 1, ResourceVersion: "1"},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "demo"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "demo"}},
			},
		},
		// ObservedGeneration=1 but UpdatedReplicas=0: the rollout is not
		// complete yet; otherwise Watch() would return successfully before
		// even starting the pod stream, and the test would not exercise the
		// loop.
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1},
	}
}

func watchHealthyPod() *corev1.Pod {
	return &corev1.Pod{
		// ResourceVersion is set explicitly because the fake clientset never
		// assigns one itself (neither here nor on Update, unlike a real
		// apiserver): without a non-empty value on objects returned by the
		// watch, watchtools.RetryWatcher treats them as a fatal error and
		// stops the stream (see retrywatcher.go, resourceVersionGetter).
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: testNamespace, Labels: map[string]string{"app": "demo"}, ResourceVersion: "1"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "app",
				Ready: true,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}
}

func updatePodToCrashLoop(t *testing.T, client kubernetes.Interface) {
	t.Helper()
	ctx := context.Background()
	p, err := client.CoreV1().Pods(testNamespace).Get(ctx, "app-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading fixture pod: %v", err)
	}
	// The fake clientset does not increment resourceVersion itself on
	// Update: advance it manually for the same reason it is set in
	// watchHealthyPod.
	p.ResourceVersion = "2"
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137,
		}},
	}}
	if _, err := client.CoreV1().Pods(testNamespace).UpdateStatus(ctx, p, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating pod to CrashLoopBackOff: %v", err)
	}
}

func updatePodRestartCount(t *testing.T, client kubernetes.Interface, restartCount int32) {
	t.Helper()
	ctx := context.Background()
	p, err := client.CoreV1().Pods(testNamespace).Get(ctx, "app-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading fixture pod: %v", err)
	}
	p.ResourceVersion = "2"
	p.Status.ContainerStatuses[0].RestartCount = restartCount
	if _, err := client.CoreV1().Pods(testNamespace).UpdateStatus(ctx, p, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating pod restart count: %v", err)
	}
}

func waitForPodWatch(t *testing.T, client *fake.Clientset) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, action := range client.Actions() {
			if action.GetVerb() == "watch" && action.GetResource().Resource == "pods" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Watch did not establish the pod stream")
}

// TestWatch_CrashLoopStopsObservation verifies the loop end-to-end: a pod
// entering CrashLoopBackOff after Watch() starts must produce an
// unsuccessful Outcome with a classified cause through the fake
// clientset's real watch stream (Create/Update emit events to registered
// watchers, as on a real apiserver). The update repeats at intervals until
// the loop observes it instead of guessing one instant when the watch is
// already established; otherwise the test would be flaky due to the race
// between watch registration and the first update.
func TestWatch_CrashLoopStopsObservation(t *testing.T) {
	client := fake.NewSimpleClientset(watchDeployment(), watchHealthyPod())
	withListResourceVersion(client, "1")
	wt := diagnose.WatchTarget{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(watchDeployment()),
		Client:    client,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		outcome diagnose.Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := diagnose.Watch(ctx, wt)
		done <- result{outcome, err}
	}()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatalf("Watch returned an unexpected error: %v", r.err)
			}
			if r.outcome.Succeeded {
				t.Fatal("Watch reported success; expected a diagnosed failure")
			}
			if !diagnose.AnyFindings(r.outcome.Results) {
				t.Fatalf("expected at least one finding in Results, got %+v", r.outcome.Results)
			}
			return
		case <-ctx.Done():
			t.Fatal("Watch did not detect CrashLoopBackOff before the timeout")
		case <-ticker.C:
			updatePodToCrashLoop(t, client)
		}
	}
}

// TestWatch_RolloutAlreadyComplete verifies that Watch() returns success if
// the rollout is already complete and remains stable for the entire
// stability window (see rolloutStabilityWindow): one "complete" read is
// necessary but no longer sufficient on its own; it must also persist.
func TestWatch_RolloutAlreadyComplete(t *testing.T) {
	d := watchDeployment()
	d.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 1,
		UpdatedReplicas:    1,
		Replicas:           1,
		AvailableReplicas:  1,
	}
	client := fake.NewSimpleClientset(d, watchHealthyPod())
	withListResourceVersion(client, "1")
	wt := diagnose.WatchTarget{
		Namespace:       testNamespace,
		Workload:        workload.FromDeployment(d),
		Client:          client,
		StabilityWindow: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	outcome, err := diagnose.Watch(ctx, wt)
	if err != nil {
		t.Fatalf("Watch returned an unexpected error: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("expected Succeeded=true for an already complete rollout, got %+v", outcome)
	}
}

// TestWatch_TransientlyComplete_IsNotEnoughOnItsOwn verifies the real bug
// found in e2e scenarios (test/e2e): a pod without a readinessProbe counts
// as Ready (and therefore Available) when the kubelet moves it to Running,
// even if it crashes moments later. If RolloutComplete() were sufficient
// on its own, Watch() would declare success for a rollout about to enter
// CrashLoopBackOff.
func TestWatch_TransientlyComplete_IsNotEnoughOnItsOwn(t *testing.T) {
	d := watchDeployment()
	d.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 1,
		UpdatedReplicas:    1,
		Replicas:           1,
		AvailableReplicas:  1,
	}
	client := fake.NewSimpleClientset(d, watchHealthyPod())
	withListResourceVersion(client, "1")
	wt := diagnose.WatchTarget{
		Namespace:       testNamespace,
		Workload:        workload.FromDeployment(d),
		Client:          client,
		StabilityWindow: 500 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		outcome diagnose.Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := diagnose.Watch(ctx, wt)
		done <- result{outcome, err}
	}()

	// The crash arrives well before the stability window expires (500ms
	// here): if the bug returned, Watch() would already have declared
	// success in the meantime.
	time.Sleep(50 * time.Millisecond)
	updatePodToCrashLoop(t, client)

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Watch returned an unexpected error: %v", r.err)
		}
		if r.outcome.Succeeded {
			t.Fatal("Watch reported success: the stability window did not catch the crash after the Complete read")
		}
		if !diagnose.AnyFindings(r.outcome.Results) {
			t.Fatalf("expected at least one finding in Results, got %+v", r.outcome.Results)
		}
	case <-ctx.Done():
		t.Fatal("Watch did not finish before the timeout")
	}
}

// TestWatch_RestartIncreaseResetsStabilityWindow verifies that a Deployment
// remaining Complete cannot reuse quiet time accumulated before a container
// restart. The new window may report success afterward because the test
// override deliberately applies to both production window variants.
func TestWatch_RestartIncreaseResetsStabilityWindow(t *testing.T) {
	d := watchDeployment()
	d.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 1,
		UpdatedReplicas:    1,
		Replicas:           1,
		AvailableReplicas:  1,
	}
	client := fake.NewSimpleClientset(d, watchHealthyPod())
	withListResourceVersion(client, "1")
	const window = 400 * time.Millisecond
	wt := diagnose.WatchTarget{
		Namespace:       testNamespace,
		Workload:        workload.FromDeployment(d),
		Client:          client,
		StabilityWindow: window,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		outcome diagnose.Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := diagnose.Watch(ctx, wt)
		done <- result{outcome, err}
	}()

	waitForPodWatch(t, client)
	time.Sleep(250 * time.Millisecond)
	updatePodRestartCount(t, client, 1)

	// The original timer expires about 150ms after this update. Waiting
	// longer than that, but less than a fresh window, distinguishes a reset
	// from the false success without depending on exact scheduler timing.
	select {
	case r := <-done:
		t.Fatalf("Watch returned before a full quiet window after the restart: outcome=%+v err=%v", r.outcome, r.err)
	case <-time.After(250 * time.Millisecond):
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Watch returned an unexpected error: %v", r.err)
		}
		if !r.outcome.Succeeded {
			t.Fatalf("expected success after the replacement quiet window, got %+v", r.outcome)
		}
	case <-ctx.Done():
		t.Fatal("Watch did not report success after the replacement quiet window")
	}
}

func TestWatch_PreexistingRestartCountDoesNotPreventSuccess(t *testing.T) {
	d := watchDeployment()
	d.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 1,
		UpdatedReplicas:    1,
		Replicas:           1,
		AvailableReplicas:  1,
	}
	p := watchHealthyPod()
	p.Status.ContainerStatuses[0].RestartCount = 3
	client := fake.NewSimpleClientset(d, p)
	withListResourceVersion(client, "1")
	wt := diagnose.WatchTarget{
		Namespace:       testNamespace,
		Workload:        workload.FromDeployment(d),
		Client:          client,
		StabilityWindow: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outcome, err := diagnose.Watch(ctx, wt)
	if err != nil {
		t.Fatalf("Watch returned an unexpected error: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("an unchanged preexisting restart count must permit success, got %+v", outcome)
	}
}

// TestWatch_ObservedRestartRequiresLongerQuietPeriod uses production windows:
// after a restart, waiting longer than the clean 5s window must still not be
// enough for success. The context is canceled instead of waiting for the full
// 30s production quiet period.
func TestWatch_ObservedRestartRequiresLongerQuietPeriod(t *testing.T) {
	d := watchDeployment()
	d.Status = appsv1.DeploymentStatus{
		ObservedGeneration: 1,
		UpdatedReplicas:    1,
		Replicas:           1,
		AvailableReplicas:  1,
	}
	client := fake.NewSimpleClientset(d, watchHealthyPod())
	withListResourceVersion(client, "1")
	wt := diagnose.WatchTarget{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		outcome diagnose.Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := diagnose.Watch(ctx, wt)
		done <- result{outcome, err}
	}()

	waitForPodWatch(t, client)
	updatePodRestartCount(t, client, 1)
	select {
	case r := <-done:
		cancel()
		t.Fatalf("Watch returned after only the clean rollout window: outcome=%+v err=%v", r.outcome, r.err)
	case <-time.After(5500 * time.Millisecond):
	}

	cancel()
	r := <-done
	if r.err != context.Canceled {
		t.Fatalf("Watch returned %v after cancellation, expected context.Canceled", r.err)
	}
}

// TestWatch_UndeterminedRefinesWithinGraceWindow verifies the real bug
// found in the ImagePullBackOff e2e scenario: the kubelet sets
// Waiting=ErrImagePull on the Pod before the Event with Reason=Failed and
// its detailed message become visible via List. If Watch finalized
// "-undetermined" on the first tick, it would miss a specific cause that
// arrives moments later. The grace window must give evidence time to
// arrive, not merely wait and still report "undetermined".
func TestWatch_UndeterminedRefinesWithinGraceWindow(t *testing.T) {
	p := watchHealthyPod()
	p.UID = "app-1-uid"
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "app",
		Image: "example.com/app:v1",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	}}
	d := watchDeployment()
	client := fake.NewSimpleClientset(d, p)
	withListResourceVersion(client, "1")
	wt := diagnose.WatchTarget{
		Namespace:               testNamespace,
		Workload:                workload.FromDeployment(d),
		Client:                  client,
		UndeterminedGraceWindow: 2 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	type result struct {
		outcome diagnose.Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := diagnose.Watch(ctx, wt)
		done <- result{outcome, err}
	}()

	// Short wait, well within the 2s grace window: create the event that
	// refines the cause and touch the pod (same condition, only to trigger
	// another evaluation through the stream).
	time.Sleep(200 * time.Millisecond)
	ctxBg := context.Background()
	_, err := client.CoreV1().Events(testNamespace).Create(ctxBg, &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "app-1.pullfailed", Namespace: testNamespace},
		InvolvedObject: corev1.ObjectReference{UID: p.UID, Namespace: testNamespace},
		Reason:         "Failed",
		Message:        `Failed to pull image "example.com/app:v1": rpc error: manifest unknown`,
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating Failed event: %v", err)
	}
	fresh, err := client.CoreV1().Pods(testNamespace).Get(ctxBg, "app-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading fixture pod: %v", err)
	}
	fresh.ResourceVersion = "2"
	if _, err := client.CoreV1().Pods(testNamespace).UpdateStatus(ctxBg, fresh, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating pod to force another evaluation: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Watch returned an unexpected error: %v", r.err)
		}
		findings := diagnose.AllFindings(r.outcome.Results)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		if findings[0].Undetermined || findings[0].CheckID != string(diagnose.CauseImagePullTagNotFound) {
			t.Fatalf("the cause should have been refined within the grace window, got %+v", findings[0])
		}
	case <-ctx.Done():
		t.Fatal("Watch did not finish before the timeout")
	}
}

func TestWatch_ProgressDeadlineObservedOnDeployment(t *testing.T) {
	d := watchDeployment()
	client := fake.NewSimpleClientset(d, watchHealthyPod())
	withListResourceVersion(client, "1")
	wt := diagnose.WatchTarget{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		outcome diagnose.Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := diagnose.Watch(ctx, wt)
		done <- result{outcome, err}
	}()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatalf("Watch returned an unexpected error: %v", r.err)
			}
			findings := diagnose.AllFindings(r.outcome.Results)
			if len(findings) != 1 || findings[0].CheckID != string(diagnose.CauseProgressDeadlineExceeded) {
				t.Fatalf("expected progress-deadline-exceeded, got %+v", findings)
			}
			return
		case <-ctx.Done():
			t.Fatal("Watch did not observe progressDeadlineExceeded on the Deployment")
		case <-ticker.C:
			current, err := client.AppsV1().Deployments(testNamespace).Get(ctx, "app", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("reading fixture Deployment: %v", err)
			}
			current.ResourceVersion = "2"
			current.Status.Conditions = []appsv1.DeploymentCondition{{
				Type:   appsv1.DeploymentProgressing,
				Status: corev1.ConditionFalse,
				Reason: "ProgressDeadlineExceeded",
			}}
			if _, err := client.AppsV1().Deployments(testNamespace).UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
				t.Fatalf("updating fixture Deployment: %v", err)
			}
		}
	}
}

func TestWatch_EventsInaccessible_DegradesWithoutLosingStructuredSignals(t *testing.T) {
	p := watchHealthyPod()
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137,
		}},
	}}
	d := watchDeployment()
	client := fake.NewSimpleClientset(d, p)
	client.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", nil)
	})

	outcome, err := diagnose.Watch(t.Context(), diagnose.WatchTarget{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("inaccessible Events must degrade, not fail the watch: %v", err)
	}
	findings := diagnose.AllFindings(outcome.Results)
	if len(findings) != 1 || findings[0].CheckID != string(diagnose.CauseCrashLoopOOMKilled) {
		t.Fatalf("lost structured OOMKilled signal: %+v", findings)
	}
	foundSkip := false
	for _, result := range outcome.Results {
		if result.DiagnoserID == "events" && result.Skipped {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("missing Result.Skipped for inaccessible Events: %+v", outcome.Results)
	}
}

// runWatch's --timeout flag (cmd/kubectl-safe_rollout) relies on Watch
// propagating context.DeadlineExceeded rather than swallowing it or hanging
// past the deadline. Reproduces the real gap found running `watch` against a
// Deployment paused mid-rollout on kind, before the Paused Diagnoser existed:
// with nothing for any Diagnoser to classify and the rollout genuinely not
// complete, Watch had no way to stop on its own.
func TestWatch_ContextDeadlineExceeded_ReturnsContextError(t *testing.T) {
	d := watchDeployment() // never reaches RolloutComplete(): see its own comment.
	client := fake.NewSimpleClientset(d, watchHealthyPod())
	withListResourceVersion(client, "1")
	wt := diagnose.WatchTarget{
		Namespace:       testNamespace,
		Workload:        workload.FromDeployment(d),
		Client:          client,
		StabilityWindow: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := diagnose.Watch(ctx, wt)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Watch took %s to return after the deadline: it must stop promptly, not linger", elapsed)
	}
}
