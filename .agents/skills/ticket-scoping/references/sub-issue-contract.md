# [Contract] <schema or path change>

> Sub-issue of `<parent-id>`. Label: `contract`. Only for repos with a
> contract-first package; elsewhere the shape change folds into the backend
> sub-issue.

## Requirements

<!-- Numbered and checkable. Name the exact schemas/paths added or changed in the contract source, and every field's type, required-ness, and the glossary entity it represents. -->

1. <requirement>
2. <requirement>

## Implementation plan

<!-- The decisions, not a tutorial: which schemas/paths in the contract source change, and why this shape (not an alternative) matches the parent's Scope. Every non-obvious decision here was pressure-tested with the requester, not assumed. -->

- `<contract source path>` — <schemas/paths touched>.
- Regen: `<generate command>` regenerates the server and client bindings — never hand-edit a generated file.
- Verify: regen produces the expected generated-file diff and nothing else (no unrelated schema drift).
