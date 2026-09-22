# [Infra] <resource change>

> Sub-issue of `<parent-id>`. Label: `infra`.

## Requirements

<!-- Numbered and checkable. Name the exact resource changed — table/index, function config, route, queue — and the access pattern it exists to serve. -->

1. <requirement>
2. <requirement>

## Implementation plan

<!-- The decisions, not a tutorial: the IaC resource(s) touched, and why this shape matches the access pattern the backend slice needs. Every non-obvious decision here was pressure-tested with the requester, not assumed. -->

- `<IaC stack path>` — <resource(s) touched>.
- Verify: the infra package's typecheck and tests pass; note any change that also needs a plan/synth or deploy check to prove.
