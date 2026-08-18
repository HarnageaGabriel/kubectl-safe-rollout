# Output conventions

- **Two formats, one Report**: check and diagnose convert their Results into
  `output.Group`; `internal/output.Report` feeds both renderers (`RenderHuman`,
  `RenderJSON`). There is no separate renderer for `watch`.
- **Human by default, explicit `--output json`**: never the other way around.
  Someone running the command manually during an incident should not have to
  read JSON.
- **Descending severity order**: always High before Low, in both formats.
  Someone reading under pressure sees the first lines first.
- **`Skipped` is as visible as a finding**, not a silent detail: human output
  prints it as a `SKIP` line with the reason and never omits it. A check that
  is silent because it could not evaluate must not look like a check that
  evaluated and found everything fine.
- **Every finding has either concrete remediation or explicitly declared
  `ContextDependent: true`** (a project quality criterion; see
  `internal/model/finding.go`). There is no third case where a command is
  printed "for reference" without the flag.
- **Explicit uncertainty**: insufficient evidence uses a
  `*-undetermined` CauseID, sets `Finding.Undetermined=true`, and lists what was
  observed. Never choose the most likely cause.
- **Exit code**: non-zero if and only if the final report contains at least one
  `SeverityHigh` finding, regardless of output format. This makes `check`
  usable as a CI/CD gate. Medium/Low severity does not change the exit code: it
  provides context, not a reason to block the pipeline.
- **Messages are in English**, because the plugin is published to the krew
  index and read by an international audience. Any future language change or
  internationalization must be a deliberate decision with a dedicated issue,
  not gradual multilingual drift.
