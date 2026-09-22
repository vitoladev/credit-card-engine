# [Backend] <endpoint or behavior>

> Sub-issue of `<parent-id>`. Label: `backend`.

## Requirements

<!-- Numbered and checkable — each is a line backend-executor's done-bar and the verify gate trace back to. Name the contract shapes, status codes, and the glossary entities/fields involved explicitly (the exact entity.field, not "the timestamp"). -->

1. <requirement>
2. <requirement>

## Implementation plan

<!-- The decisions, not a tutorial: which files, which store seam (the in-memory / real-backing-store pair under the owning domain package), what the test must prove. Every non-obvious decision here was pressure-tested with the requester, not assumed. -->

- `<backend package path>` — <what lands here and why this seam>.
- Test: <the behavior the test must prove, at the handler boundary unless there's a reason not to>.
- Verify: `<command that exercises the endpoint>` → <expected output>.
