# kubectl safe-rollout

Plugin `kubectl` che riduce il tempo tra "il deploy e' fallito" e "so
perche' e come lo sistemo".

> **Stato: MVP in sviluppo.** `check` include `pdb-consistency`; `watch`
> osserva Deployment e Pod e classifica deterministicamente le cause MVP,
> verificato con 11 scenari e2e su kind (`make test-e2e`). Demo/asciinema
> restano da completare.

## Cosa fa

- `kubectl safe-rollout check <kind>/<name>` — analisi pre-flight del
  workload live e del contesto namespace.
- `kubectl safe-rollout watch <kind>/<name>` — osserva il rollout; in caso
  di fallimento o stallo raccoglie stato, Event e log precedenti, classifica
  la causa e propone remediation basata sull'evidenza.

`watch` distingue:

- CrashLoopBackOff: OOMKilled, liveness probe, exit applicativo;
- ImagePullBackOff: tag/digest inesistente, registry irraggiungibile,
  credenziali mancanti;
- Pending: risorse insufficienti, quota, vincoli scheduling, PVC non Bound;
- `ProgressDeadlineExceeded`.

Evidenza insufficiente produce una diagnosi `*-undetermined`, mai una causa
probabile presentata come certa.

Il tool non modifica mai il cluster. Non e' dashboard, SaaS, cost reporting,
ne' sostituto di Argo Rollouts o Flagger.

## Build

Richiede Go 1.26+.

```bash
go build -o bin/kubectl-safe_rollout ./cmd/kubectl-safe_rollout
```

## Uso

```bash
go run ./cmd/kubectl-safe_rollout check deployment/checkout
go run ./cmd/kubectl-safe_rollout check deployment/checkout --output json
go run ./cmd/kubectl-safe_rollout watch deployment/checkout
go run ./cmd/kubectl-safe_rollout watch deployment/checkout --output json
```

Rispetta kubeconfig, context e namespace correnti, inclusi `--context`,
`--namespace` e `--kubeconfig`. Exit code non zero sui finding High.

## Sviluppo

- [docs/watch-vs-polling.md](docs/watch-vs-polling.md) — scelta Watch API.
- [rules/](rules/) — convenzioni Go, test, output, commit e licenza.
- `make test`, `make lint`, `make cover` — ciclo locale.
- `make kind-up` poi `make test-e2e` — scenari e2e reali su kind
  (richiede Docker). `make kind-down` per eliminare il cluster.

## Licenza

Apache License 2.0, vedi [LICENSE](LICENSE). Nessun NOTICE: il repository
non vendorizza opere derivate con attribuzioni NOTICE. Motivazione completa
in [rules/license.md](rules/license.md).

## Contesto

Progetto open source personale. Nessun codice, configurazione o pattern
proveniente da progetti aziendali entra nel repository.
