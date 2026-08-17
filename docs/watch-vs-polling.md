# Decisione: Watch API, non polling

Decisione architetturale chiusa prima dell'implementazione di `watch`.

## Decisione

`watch` usa la Watch API Kubernetes per Pod e Deployment del workload.
Parte da una `List` dei Pod e da un `Get` del Deployment, entrambi con
`resourceVersion`, poi apre due `RetryWatcher`.

Non usa polling a intervallo fisso.

## Motivazioni

- **Reattivita'**: l'apiserver notifica le transizioni appena avvengono.
  Un intervallo di polling aggiungerebbe latenza e potrebbe perdere stati
  brevi.
- **Carico**: i watch inviano delta. Il polling ripeterebbe `List` anche
  senza cambiamenti, costo inutile su cluster o namespace grandi.
- **ProgressDeadlineExceeded**: viene osservato sul Deployment, non
  inferito dai Pod. Il secondo stream evita di attendere un evento Pod
  che potrebbe non arrivare quando cambia solo `Deployment.Status`.
- **Scope limitato**: il selector restringe i Pod al workload; il field
  selector restringe il Deployment al nome richiesto.

## Riconnessione

`RetryWatcher` riapre lo stream dalla ultima `resourceVersion` per errori
recuperabili, inclusi EOF, timeout e riavvio dell'apiserver. Non esegue
una nuova List quando la resourceVersion e' scaduta: in caso HTTP 410
termina lo stream con un evento Error. Il loop esterno intercetta quel
caso, rifà List/Get e ricrea entrambi i watch da snapshot coerenti.

Errori non recuperabili, inclusi Forbidden e Unauthorized, vengono
restituiti esplicitamente. Nessun fallback silenzioso a polling.

## Perche' non SharedInformer

`watch` vive per un singolo rollout. Non serve una cache condivisa,
un indexer o un processo controller long-running. `RetryWatcher` piu'
re-list esplicita su HTTP 410 forniscono la semantica necessaria con
meno stato e meno superficie di errore.

## Letture puntuali durante la diagnosi

Ogni evento Pod o Deployment causa una rivalutazione:

- `Get` del Deployment per successo e `ProgressDeadlineExceeded`;
- `List` dei ReplicaSet posseduti, per eventi `FailedCreate` di quota;
- `List` degli Event del namespace, correlati tramite UID;
- `Get` delle sole PVC referenziate da Pod Pending;
- log `--previous` limitati, solo per exit code applicativo.

ReplicaSet ed Event non hanno stream separati nell'MVP. Questa scelta
evita la correlazione di quattro stream indipendenti. Se misure su cluster
grandi mostrano carico eccessivo, prima ottimizzazione sara' una cache
locale degli Event o un watch filtrato, non polling.

## Finestre temporali: stabilita' e grazia

Due meccanismi aggiunti dopo aver eseguito gli scenari e2e su kind
(`test/e2e/`), entrambi bug reali emersi solo contro un apiserver vero,
mai visibili con fake clientset:

- **rolloutStabilityWindow (5s)**: un container senza `readinessProbe`
  conta come Ready appena il kubelet lo porta Running, anche per un
  istante prima di crashare. `RolloutComplete()` letto una sola volta
  poteva quindi dichiarare successo su un rollout che un momento dopo
  andava in `CrashLoopBackOff`. Ora la lettura "completo" deve restare
  vera ininterrottamente per la finestra prima di essere un successo
  definitivo.
- **undeterminedGraceWindow (3s) + gracePollInterval (500ms)**: un Pod in
  `ImagePullBackOff` puo' mostrare quello stato prima ancora che l'Event
  `Reason=Failed` col messaggio dettagliato sia visibile via `List`.
  Fermarsi al primo tick avrebbe riportato "non determinato" anche
  quando la causa specifica stava per arrivare. Quando ogni Finding del
  tick e' `Undetermined`, `watch` ripete `evaluateTick` (che rilegge
  Deployment/ReplicaSet/Event) ogni `gracePollInterval` finche' non
  emerge una causa determinata o scade `undeterminedGraceWindow` — a quel
  punto "non determinato" e' l'esito onesto, non piu' una lettura
  prematura.

`gracePollInterval` e' l'unica eccezione dichiarata al principio
push-driven di questo documento: attiva solo mentre una causa resta
ambigua, mai oltre `undeterminedGraceWindow`. Non e' una deriva verso il
polling come meccanismo primario — il trigger che porta `watch` a
fermarsi resta comunque un evento reale (Pod, Deployment, o la causa che
si affina), non un timer che decide da solo.

Entrambe le finestre sono override-abili per i test
(`WatchTarget.StabilityWindow`, `WatchTarget.UndeterminedGraceWindow`):
`cmd/kubectl-safe_rollout` non le imposta mai, quindi l'uso reale ottiene
sempre i default di produzione.

## Limiti di verifica attuali

I fake clientset verificano transizioni Pod/Deployment, classificazione
e wiring della Watch API. Non simulano fedelmente compaction etcd,
riconnessioni TCP, HTTP 410 reale, carico elevato o varianti runtime dei
messaggi Event. Questi casi richiedono cluster reali e test kind.
