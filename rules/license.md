# License

- **Apache License 2.0**, complete and unmodified text in
  [LICENSE](../LICENSE). Chosen for consistency with the CNCF ecosystem this
  project aims to join (Kubernetes, client-go, cli-runtime, krew-index, and
  nearly all tools in the kubectl plugin ecosystem use Apache 2.0): it reduces
  friction for anyone who wants to vendor, contribute to, or redistribute the
  plugin alongside the rest of their toolchain.
- **No NOTICE file.** The license does not require one unless redistributing a
  derivative work that already includes its own NOTICE (Section 4(d) of the
  license): this repository neither vendors nor copies third-party source;
  dependencies remain external through `go.mod`/`go.sum` and carry their own
  licenses. If the project vendors third-party code with its own NOTICE in the
  future, this decision must be revisited here, not silently bypassed.
- **Boilerplate header in every `.go` file**, applied uniformly, not
  partially: every source file, including tests, carries the standard header
  from the license appendix (see the top of any file, for example
  [internal/model/finding.go](../internal/model/finding.go)). Enforced in CI by
  the `goheader` linter (see [.golangci.yml](../.golangci.yml)): a new file
  without the header makes `make lint` fail; it is not left to the discipline
  of the person writing the commit.
- **Year in the header**: the template uses the `goheader` placeholder
  `{{ YEAR }}`, not a literal year. A file written in a year other than that of
  the first commit remains valid without changing the configuration.
- **Copyright holder**: Gabriel Harnagea (project author). If the project gains
  coauthors with substantial contributions, this decision must be explicitly
  reconsidered rather than leaving the first name in place through inertia.
