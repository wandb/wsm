<!--
PR TITLE must follow Conventional Commits + Graphite style, e.g.
  feat(deploy-v2): add kind cluster picker
  • format: type(scope)!: imperative, lowercase description (no trailing period, <= 72 chars)
  • types: feat fix build chore ci docs perf refactor revert style test
  • https://www.conventionalcommits.org/en/v1.0.0/
  • https://graphite.com/guides/best-pr-title-guidelines

RELEASE LABEL required — the PR Checks gate fails without exactly one of:
  release:major  release:minor  release:patch  release:skip
release-on-merge.yaml reads it to decide the version bump cut on merge to main.
-->

# Summary of Changes

**Jira:** [ONPREM-XXXX](https://coreweave.atlassian.net/browse/ONPREM-XXXX)

<!-- What does this PR do and why? Minimum 50 characters of real content. -->

# Test Plan

<!--
How was this tested? There is no unit-test suite; give the actual commands/steps:
  • make build          — compiles clean
  • make lint           — go vet + golangci-lint clean
  • ./wsm list          — v1 sanity, no cluster needed
  • Kind smoke test     — v2 (deploy-v2 operator / wandb deploy) against a local cluster
-->

# Requirements

<!-- Every box must be checked before this PR can merge. -->

- [ ] Lint clean (`make lint`)
- [ ] Format clean (`make fmt` leaves no diff)
- [ ] `go.mod` / `go.sum` tidy (`go mod tidy` leaves no diff)
- [ ] I/AI have tested the changes on this PR
- [ ] Docs (`docs/`, `docs/reference/commands.md`, `docs/reference/cr-fields.md`) / `CLAUDE.md` updated if behavior changed
