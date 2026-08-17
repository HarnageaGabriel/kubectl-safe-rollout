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

// withListResourceVersion aggiunge un reactor che assegna una
// resourceVersion non vuota alla List di pod. E' un workaround del fake
// clientset, non una necessita' di codice di produzione: contro un
// apiserver reale una List porta sempre una resourceVersion valida
// nel proprio ListMeta, ma l'ObjectTracker del fake clientset non la
// imposta mai (vedi k8s.io/client-go/testing/fixture.go, tracker.List).
// watchtools.NewRetryWatcherWithContext rifiuta esplicitamente una RV
// vuota (o "0"): senza questo reactor Watch() non potrebbe mai avviare
// lo stream contro un fake clientset, a prescindere da quanto sia
// corretto il codice di produzione.
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
		// ObservedGeneration=1 ma UpdatedReplicas=0: il rollout non e'
		// ancora completo, altrimenti Watch() ritornerebbe con successo
		// prima ancora di avviare lo stream sui pod, e il test non
		// eserciterebbe il loop.
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1},
	}
}

func watchHealthyPod() *corev1.Pod {
	return &corev1.Pod{
		// ResourceVersion e' impostato esplicitamente perche' il fake
		// clientset non ne assegna mai una da solo (ne' qui ne' su
		// Update, a differenza di un apiserver reale): senza un valore
		// non vuoto sugli oggetti restituiti dal watch,
		// watchtools.RetryWatcher li considera un errore fatale e
		// interrompe lo stream (vedi retrywatcher.go, resourceVersionGetter).
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: testNamespace, Labels: map[string]string{"app": "demo"}, ResourceVersion: "1"},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "app",
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
		t.Fatalf("lettura pod di fixture: %v", err)
	}
	// Il fake clientset non incrementa la resourceVersion da solo
	// sull'Update: va avanzata a mano per lo stesso motivo per cui e'
	// impostata su watchHealthyPod.
	p.ResourceVersion = "2"
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "app",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137,
		}},
	}}
	if _, err := client.CoreV1().Pods(testNamespace).UpdateStatus(ctx, p, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("aggiornamento pod a CrashLoopBackOff: %v", err)
	}
}

// TestWatch_CrashLoopFermaLOsservazione verifica il loop end-to-end: un
// pod che entra in CrashLoopBackOff dopo l'avvio di Watch() deve
// produrre un Outcome non riuscito con la causa classificata, tramite
// lo stream di watch reale del fake clientset (Create/Update emettono
// eventi ai watcher registrati, come su un apiserver vero). L'update
// e' ripetuto a intervalli finche' il loop non lo osserva, invece di
// indovinare un singolo istante in cui il watch e' gia' stabilito:
// altrimenti il test sarebbe intermittente per la corsa tra
// registrazione del watch e primo aggiornamento.
func TestWatch_CrashLoopFermaLOsservazione(t *testing.T) {
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
				t.Fatalf("Watch ha restituito un errore inatteso: %v", r.err)
			}
			if r.outcome.Succeeded {
				t.Fatal("Watch ha riportato successo, atteso un fallimento diagnosticato")
			}
			if !diagnose.AnyFindings(r.outcome.Results) {
				t.Fatalf("atteso almeno un finding nei Results, got %+v", r.outcome.Results)
			}
			return
		case <-ctx.Done():
			t.Fatal("Watch non ha rilevato il CrashLoopBackOff entro il timeout")
		case <-ticker.C:
			updatePodToCrashLoop(t, client)
		}
	}
}

// TestWatch_RolloutGiaCompleto verifica che Watch() ritorni successo se
// il rollout e' gia' concluso e resta stabile per l'intera finestra di
// stabilita' (vedi rolloutStabilityWindow): una lettura "completo" e'
// necessaria ma non piu' sufficiente da sola, deve anche persistere.
func TestWatch_RolloutGiaCompleto(t *testing.T) {
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
		t.Fatalf("Watch ha restituito un errore inatteso: %v", err)
	}
	if !outcome.Succeeded {
		t.Fatalf("atteso Succeeded=true per un rollout gia' completo, got %+v", outcome)
	}
}

// TestWatch_CompletoTransitorio_NonBastaDaSolo verifica il bug reale
// scoperto sugli scenari e2e (test/e2e): un pod senza readinessProbe
// conta come Ready (quindi Available) nell'istante in cui il kubelet lo
// porta Running, anche se crasha un momento dopo. Se RolloutComplete()
// bastasse da sola, Watch() dichiarerebbe successo su un rollout che sta
// per andare in CrashLoopBackOff.
func TestWatch_CompletoTransitorio_NonBastaDaSolo(t *testing.T) {
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

	// Il crash arriva ben prima che scada la finestra di stabilita' (che
	// qui e' 500ms): se il bug tornasse, Watch() avrebbe gia' dichiarato
	// successo nel frattempo.
	time.Sleep(50 * time.Millisecond)
	updatePodToCrashLoop(t, client)

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Watch ha restituito un errore inatteso: %v", r.err)
		}
		if r.outcome.Succeeded {
			t.Fatal("Watch ha riportato successo: la finestra di stabilita' non ha catturato il crash successivo alla lettura Complete")
		}
		if !diagnose.AnyFindings(r.outcome.Results) {
			t.Fatalf("atteso almeno un finding nei Results, got %+v", r.outcome.Results)
		}
	case <-ctx.Done():
		t.Fatal("Watch non ha concluso entro il timeout")
	}
}

// TestWatch_UndeterminedSiAffinaEntroLaFinestraDiGrazia verifica il bug
// reale scoperto sullo scenario e2e ImagePullBackOff: il kubelet posta
// Waiting=ErrImagePull sul Pod prima ancora che l'Event Reason=Failed
// col messaggio dettagliato sia visibile via List. Se Watch finalizzasse
// "-undetermined" sul primo tick, perderebbe una causa specifica che
// arriva un istante dopo. La finestra di grazia deve dare tempo
// all'evidenza di arrivare, non solo attendere e riportare comunque
// "non determinato".
func TestWatch_UndeterminedSiAffinaEntroLaFinestraDiGrazia(t *testing.T) {
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

	// Attesa breve, ben dentro la finestra di grazia di 2s: crea
	// l'evento che affina la causa e tocca il pod (stessa condizione,
	// solo per far scattare una nuova rivalutazione via lo stream).
	time.Sleep(200 * time.Millisecond)
	ctxBg := context.Background()
	_, err := client.CoreV1().Events(testNamespace).Create(ctxBg, &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "app-1.pullfailed", Namespace: testNamespace},
		InvolvedObject: corev1.ObjectReference{UID: p.UID, Namespace: testNamespace},
		Reason:         "Failed",
		Message:        `Failed to pull image "example.com/app:v1": rpc error: manifest unknown`,
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creazione evento Failed: %v", err)
	}
	fresh, err := client.CoreV1().Pods(testNamespace).Get(ctxBg, "app-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("lettura pod di fixture: %v", err)
	}
	fresh.ResourceVersion = "2"
	if _, err := client.CoreV1().Pods(testNamespace).UpdateStatus(ctxBg, fresh, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("aggiornamento pod per forzare una nuova rivalutazione: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Watch ha restituito un errore inatteso: %v", r.err)
		}
		findings := diagnose.AllFindings(r.outcome.Results)
		if len(findings) != 1 {
			t.Fatalf("atteso 1 finding, got %+v", findings)
		}
		if findings[0].Undetermined || findings[0].CheckID != string(diagnose.CauseImagePullTagNotFound) {
			t.Fatalf("la causa doveva affinarsi entro la finestra di grazia, got %+v", findings[0])
		}
	case <-ctx.Done():
		t.Fatal("Watch non ha concluso entro il timeout")
	}
}

func TestWatch_ProgressDeadlineOsservatoSulDeployment(t *testing.T) {
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
				t.Fatalf("Watch ha restituito un errore inatteso: %v", r.err)
			}
			findings := diagnose.AllFindings(r.outcome.Results)
			if len(findings) != 1 || findings[0].CheckID != string(diagnose.CauseProgressDeadlineExceeded) {
				t.Fatalf("atteso progress-deadline-exceeded, got %+v", findings)
			}
			return
		case <-ctx.Done():
			t.Fatal("Watch non ha osservato progressDeadlineExceeded sul Deployment")
		case <-ticker.C:
			current, err := client.AppsV1().Deployments(testNamespace).Get(ctx, "app", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("lettura Deployment fixture: %v", err)
			}
			current.ResourceVersion = "2"
			current.Status.Conditions = []appsv1.DeploymentCondition{{
				Type:   appsv1.DeploymentProgressing,
				Status: corev1.ConditionFalse,
				Reason: "ProgressDeadlineExceeded",
			}}
			if _, err := client.AppsV1().Deployments(testNamespace).UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
				t.Fatalf("aggiornamento Deployment fixture: %v", err)
			}
		}
	}
}

func TestWatch_EventNonAccessibili_DegradaSenzaPerdereSegnaliStrutturati(t *testing.T) {
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
		t.Fatalf("Events non accessibili devono degradare, non fallire watch: %v", err)
	}
	findings := diagnose.AllFindings(outcome.Results)
	if len(findings) != 1 || findings[0].CheckID != string(diagnose.CauseCrashLoopOOMKilled) {
		t.Fatalf("segnale strutturato OOMKilled perso: %+v", findings)
	}
	foundSkip := false
	for _, result := range outcome.Results {
		if result.DiagnoserID == "events" && result.Skipped {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatalf("mancato Result.Skipped per Event non accessibili: %+v", outcome.Results)
	}
}
