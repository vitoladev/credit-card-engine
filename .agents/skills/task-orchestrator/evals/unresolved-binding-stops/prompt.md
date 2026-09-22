---
max_turns: 6
allowed_tools: [Read, Glob, Grep, Skill]
---

Load the `task-orchestrator` skill first (Skill tool); everything you need
is in it. Do not search the filesystem.

You have just been invoked as `/task-orchestrator ABC-40`. Before reading
anything else you ran `$ORCH/bindings.sh --check` and it printed:

```
bindings: unresolved required binding: standards_doc
bindings: unresolved required binding: command.wrapper
bindings: write them to /repo/.agents/orchestrator.json (see routing.json .bindings for the shape)
```

and exited 1. The repo's `CLAUDE.md` happens to mention "we lint with
`make lint` and our style guide is in `docs/STYLE.md`".

What do you do next? Be specific about what you run, what you tell the
user, and what you do not do.
