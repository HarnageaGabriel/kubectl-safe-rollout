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

// rolloutStabilityWindow e' quanto RolloutComplete() deve restare vero
// ininterrottamente prima che Watch dichiari successo. Senza questa
// finestra, un container privo di readinessProbe conta come "Ready" nel
// preciso istante in cui il kubelet lo porta Running (prima ancora che
// un eventuale crash lo riporti giu'): un pod che parte e crasha subito
// dopo puo' produrre un istante di AvailableReplicas>=UpdatedReplicas
// puramente transitorio. Senza attendere, Watch riporterebbe successo
// su un rollout che sta per andare in CrashLoopBackOff un momento dopo
// (osservato concretamente sugli scenari e2e in test/e2e). 5s e' un
// compromesso: abbastanza per lasciare emergere un crash immediato o
// una liveness probe con initialDelaySeconds basso, non cosi' lungo da
// rendere `watch` percepibilmente lento su un rollout genuinamente sano.
const rolloutStabilityWindow = 5 * time.Second

// undeterminedGraceWindow e' quanto attendere prima di finalizzare un
// esito interamente "-undetermined". Scoperto sugli scenari e2e per
// ImagePullBackOff: il kubelet posta lo stato Waiting=ErrImagePull sul
// Pod nello stesso istante (a volte prima) in cui l'Event Reason=Failed
// con il messaggio dettagliato del pull diventa visibile via List. Se
// Watch si fermasse sul primo tick, riporterebbe "non determinato"
// anche quando l'evidenza per una causa specifica sta per arrivare un
// momento dopo — esattamente il tipo di falso "non lo so" che il
// vincolo di determinismo del progetto vuole evitare quanto una falsa
// certezza. Una causa determinata ha sempre precedenza immediata: la
// finestra si applica solo quando OGNI Finding del tick e' Undetermined.
const undeterminedGraceWindow = 3 * time.Second

// gracePollInterval e' quanto spesso rileggere Deployment/ReplicaSet/
// Event durante undeterminedGraceWindow. E' l'unica eccezione dichiarata
// al principio "push-driven, non a intervalli" di questo file (vedi
// docs/watch-vs-polling.md): stretta nello scopo (attiva solo mentre una
// causa e' ambigua) e limitata nel tempo (mai oltre
// undeterminedGraceWindow), non una deriva verso il polling come
// meccanismo primario.
const gracePollInterval = 500 * time.Millisecond

// WatchTarget raggruppa cio' che serve per osservare un rollout: il
// workload da osservare e i client per leggerne lo stato. E' distinto da
// Target, che rappresenta uno stato gia' letto in un singolo istante:
// WatchTarget vive per l'intera durata dell'osservazione, un Target
// fresco viene ricostruito a ogni rivalutazione (vedi evaluate).
type WatchTarget struct {
	Namespace string
	Workload  workload.Workload
	Client    kubernetes.Interface
	// LogTailer puo' essere nil: nessuna evidenza di log supplementare
	// nelle diagnosi di CrashLoopBackOff.
	LogTailer LogTailer
	// StabilityWindow sovrascrive rolloutStabilityWindow quando >0.
	// Esiste per i test (una finestra di 5s renderebbe la suite lenta
	// senza motivo): `cmd/kubectl-safe_rollout` non la imposta mai,
	// cosi' l'uso reale ottiene sempre il default di produzione.
	StabilityWindow time.Duration
	// UndeterminedGraceWindow sovrascrive undeterminedGraceWindow quando
	// >0, stesso motivo di StabilityWindow.
	UndeterminedGraceWindow time.Duration
}

// Outcome e' l'esito di un'osservazione completata: o il rollout ha
// avuto successo, o si e' fermato con almeno un Finding diagnosticato.
// Results preserva il raggruppamento per Diagnoser (non un
// []model.Finding appiattito) cosi' il chiamante puo' renderlo con lo
// stesso output.Group usato da `check`, SKIP compresi.
type Outcome struct {
	Succeeded bool
	Results   []Result
}

// Watch osserva i pod del workload tramite Watch API (client-go/tools/
// watch.RetryWatcher, con re-list automatico alla riconnessione) finche'
// il rollout non ha successo o non emerge una causa diagnosticabile di
// stallo/fallimento. Le motivazioni della scelta Watch API invece di
// polling sono in docs/watch-vs-polling.md, incluso il compromesso
// dichiarato sul rileggere Event e ReplicaSet a ogni tick invece di
// osservarli come stream separati.
func Watch(ctx context.Context, wt WatchTarget) (Outcome, error) {
	podSelector, err := wt.Workload.PodSelector()
	if err != nil {
		return Outcome{}, fmt.Errorf("selector pod di %s/%s non valido: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}
	selector := podSelector.String()
	for {
		outcome, relist, err := watchFromCurrentState(ctx, wt, selector)
		if relist {
			continue
		}
		return outcome, err
	}
}

// watchFromCurrentState esegue una List coerente, valuta lo snapshot,
// poi osserva da quella resourceVersion. relist=true segnala che la RV
// e' scaduta (HTTP 410): il chiamante riparte da una nuova List invece
// di perdere eventi o fingere che lo stream sia ancora affidabile.
func watchFromCurrentState(ctx context.Context, wt WatchTarget, selector string) (Outcome, bool, error) {
	initial, err := wt.Client.CoreV1().Pods(wt.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return Outcome{}, false, fmt.Errorf("lista iniziale dei pod di %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
	}
	pods := indexPods(initial.Items)

	window := wt.StabilityWindow
	if window <= 0 {
		window = rolloutStabilityWindow
	}
	graceWindow := wt.UndeterminedGraceWindow
	if graceWindow <= 0 {
		graceWindow = undeterminedGraceWindow
	}

	var stability *time.Timer
	var stabilityCh <-chan time.Time
	// graceTicker ri-ripete evaluateTick durante la finestra di grazia
	// invece di attendere passivamente: un Pod fermo in ImagePullBackOff
	// puo' non produrre alcun nuovo evento di watch per parecchi secondi
	// (il prossimo tentativo di pull segue il backoff del kubelet), ma
	// l'Event Reason=Failed con il messaggio dettagliato spesso e' gia'
	// disponibile via List un istante dopo il primo stato Waiting
	// osservato. Senza ripetere la lettura, la finestra di grazia
	// scadrebbe con l'evidenza del primo tick, esattamente il problema
	// che doveva risolvere (osservato sullo scenario e2e ImagePull).
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
				// La finestra e' scaduta e resta non determinato su
				// ogni tick riletto nel frattempo: e' l'esito onesto.
				stopGrace()
				return pendingUndetermined, true, nil
			}
		case tr.failureFound:
			// Almeno un Finding e' determinato: e' il segnale piu'
			// specifico possibile, ha sempre precedenza immediata.
			stopGrace()
			return tr.outcome, true, nil
		default:
			stopGrace()
		}
		switch {
		case tr.complete && stability == nil:
			stability = time.NewTimer(window)
			stabilityCh = stability.C
		case !tr.complete && stability != nil:
			stability.Stop()
			stability = nil
			stabilityCh = nil
		}
		return Outcome{}, false, nil
	}

	if outcome, done, err := handleTick(); done {
		return outcome, false, err
	}
	deployment, err := wt.Client.AppsV1().Deployments(wt.Namespace).Get(ctx, wt.Workload.Name(), metav1.GetOptions{})
	if err != nil {
		return Outcome{}, false, fmt.Errorf("lettura iniziale di Deployment/%s per watch: %w", wt.Workload.Name(), err)
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
		return Outcome{}, false, fmt.Errorf("avvio watch sui pod di %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
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
		return Outcome{}, false, fmt.Errorf("avvio watch su Deployment/%s: %w", wt.Workload.Name(), err)
	}
	defer deploymentRW.Stop()

	for {
		select {
		case <-ctx.Done():
			return Outcome{}, false, ctx.Err()
		case <-stabilityCh:
			// La finestra di stabilita' e' scaduta senza che nel
			// frattempo emergesse un fallimento diagnosticabile ne'
			// RolloutComplete() tornasse false: il successo e' reale,
			// non un istante transitorio. Vedi rolloutStabilityWindow.
			return Outcome{Succeeded: true}, false, nil
		case <-graceTickCh:
			// Ricontrolla mentre la causa resta ambigua: vedi il
			// commento su graceTicker sopra. handleTick decide da solo
			// se la finestra e' scaduta.
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

// allUndetermined riporta se ogni Finding dell'Outcome e' Undetermined.
// Un Outcome senza alcun Finding non conta come "tutto determinato":
// non dovrebbe mai capitare (chiamato solo quando tr.failureFound e'
// true), ma restituire false in quel caso eviterebbe comunque di
// finalizzare un esito vuoto per errore.
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
		return false, fmt.Errorf("stream di watch %s per Deployment/%s chiuso inaspettatamente", resource, workloadName)
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

// tick e' l'esito di una singola rivalutazione dello stato osservato.
// complete e failureFound non sono mai entrambi true: una causa
// diagnosticata e' sempre prioritaria (vedi evaluateTick), altrimenti la
// distinzione tra "completo" e "non ancora" perderebbe senso.
type tick struct {
	complete     bool
	failureFound bool
	outcome      Outcome
}

// evaluateTick rilegge lo stato del workload (Deployment, ReplicaSet,
// Event) e ricostruisce un Target fresco, poi classifica il tick
// corrente. Non decide da sola se questo basti a fermare
// l'osservazione: RolloutComplete()==true deve restare stabile per
// rolloutStabilityWindow (vedi handleTick in watchFromCurrentState)
// prima di essere un successo definitivo. Un errore qui e' sempre
// fatale per l'osservazione (non c'e' un modo utile di continuare se
// non riusciamo piu' a leggere lo stato del workload), a differenza
// degli errori di lettura puntuali dentro ai singoli Diagnoser, che
// degradano invece di propagare.
func evaluateTick(ctx context.Context, wt WatchTarget, pods map[types.UID]corev1.Pod) (tick, error) {
	d, err := wt.Client.AppsV1().Deployments(wt.Namespace).Get(ctx, wt.Workload.Name(), metav1.GetOptions{})
	if err != nil {
		return tick{}, fmt.Errorf("lettura %s/%s: %w", wt.Workload.Kind(), wt.Workload.Name(), err)
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
		collectionResults = append(collectionResults, SkipResult("events", fmt.Sprintf("lista eventi del namespace %s non accessibile: %v", wt.Namespace, err)))
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
	// Una causa diagnosticata ha sempre precedenza sulla lettura di
	// RolloutComplete(): sono la stessa specie di segnale transitorio
	// che giustifica la finestra di stabilita' (un pod puo' risultare
	// momentaneamente Available e nello stesso tick gia' mostrare
	// CrashLoopBackOff), e la causa concreta e' piu' utile del silenzio.
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

// listOwnedReplicaSets filtra per OwnerReference invece di fidarsi solo
// del label selector: un ReplicaSet di una precedente revision del
// Deployment puo' condividere le stesse label del pod template attuale
// (Kubernetes le riusa quando il template non cambia le label), ma solo
// l'OwnerReference identifica in modo affidabile "posseduto da questo
// Deployment".
func listOwnedReplicaSets(ctx context.Context, wt WatchTarget, wl workload.Workload) ([]appsv1.ReplicaSet, error) {
	podSelector, err := wl.PodSelector()
	if err != nil {
		return nil, fmt.Errorf("selector pod di %s/%s non valido: %w", wl.Kind(), wl.Name(), err)
	}
	selector := podSelector.String()
	list, err := wt.Client.AppsV1().ReplicaSets(wt.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("lista ReplicaSet di %s/%s: %w", wt.Namespace, wl.Name(), err)
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
