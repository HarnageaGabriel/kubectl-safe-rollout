# Convenzioni di commit

Conventional Commits, come la maggior parte dell'ecosistema
cloud-native con cui questo progetto vuole interagire (contributi,
changelog automatico, eventuale futura integrazione con release-please
o simili per le release krew).

```
<tipo>(<scope opzionale>): <descrizione breve in inglese, imperativo>

<corpo opzionale: perche', non cosa — il diff mostra gia' cosa>
```

Tipi usati in questo repo:

- `feat` — nuova verifica in `check`, nuova classificazione in
  `diagnose`, nuovo comando.
- `fix` — correzione di una verifica esistente (falso positivo, falso
  negativo, remediation sbagliata).
- `test` — solo test, nessuna modifica alla logica.
- `docs` — README, rules/, docs/.
- `chore` — dipendenze, CI, scaffolding.
- `refactor` — nessun cambio di comportamento osservabile.

Scope consigliati: il package toccato senza `internal/` (`check`,
`workload`, `output`, `kube`, `cmd`).

Esempi:

```
feat(check): add ResourceQuota headroom check
fix(check): handle nil selector as no match in pdb-consistency
test(check): add Recreate case with exhausted PDB budget
docs: document end-to-end test workflow
```

Un fix su un falso positivo o falso negativo di una verifica **deve**
menzionare nel corpo lo scenario concreto che lo ha esposto: e' quello
che rende il commit utile a chi debugga la stessa classe di problema
in futuro, non solo un changelog.

Ogni commit deve includere il trailer `Signed-off-by`, aggiunto con
`git commit -s`, in conformita' al Developer Certificate of Origin
(DCO). La CI rifiuta le pull request che contengono commit senza
sign-off.
