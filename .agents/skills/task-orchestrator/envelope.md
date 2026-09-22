# Write the agent envelope

Every agent the task-orchestrator dispatches ends its work by writing one
JSON file: the envelope. The orchestrator reads nothing else. Prose in the
agent's final message is context for a human. It never marks a phase done.
The schema is `scripts/envelope.schema.json` beside this file.

## Write it to the path the packet names

The dispatch packet names the exact path:

```
<state_dir>/<ticket>/envelopes/<slice>-<phase>-<attempt>.json
```

`<state_dir>` is the repo's `state_dir` binding (default
`docs/ai/executions`). `<slice>` is the sub-issue, for example `ABC-13`.
For a stack-level phase it is `stack`. The directory is gitignored. Write
the file with a heredoc or the Write tool.

## Fill every required field

Every envelope carries these fields:

| Field | Value |
| --- | --- |
| `envelope_version` | `1` |
| `ticket`, `slice`, `phase`, `agent`, `model`, `attempt` | exactly what the packet says. A mismatch is rejected as a stale file. |
| `status` | `done`, `failed`, or `blocked` |
| `summary` | one line |
| `commands` | `[{cmd, outcome, note?}]` for every command whose outcome you claim. `outcome` is `pass`, `fail`, or `skipped`. |
| `complete` | `true`, written last, only once every other field holds observed results |

Each phase requires more fields on top of those:

| Phase | Required |
| --- | --- |
| `implement`, `fix` | `files_changed`, `unsatisfied` as `[{requirement, reason}]`, `deviations` |
| `verify` | `criteria` as `[{criterion, verdict, evidence, level, invalidated_by?}]`, `findings`. `verdict` is `passed`, `failed`, or `blocked`. `level` is `test`, `spec`, `live-once`, or `controlled-response`. |
| `commit` | `commits`, full or short SHAs |
| `review` | `verdict` (`pass` or `fail`), `findings` as `[{severity, text, paths?}]` with `severity` from `P0` to `P3` |
| `fix-check` | `verdict` (`resolved`, `not-resolved`, or `introduced-new`), `findings` |

`docs_touched`, `promoted_specs`, and `blocked_reason` are optional in
every phase.

## Write `complete` last

A session limit or a context cut can kill an agent while it reports. A
file that is not valid JSON, or valid JSON without `"complete": true`, is a
truncated run. The orchestrator does not guess what the missing half said.
It validates the file, then sends you one nudge to finish the envelope
from work already done. After two failed nudges it marks the phase failed,
and a resume dispatches the phase again. Write the fields you can prove
first. Write `complete` when there is nothing left to add.

## Check the file before you stop

```
.agents/orchestrator/validate.sh envelope <path>
```

The command prints `valid`, or one violation per line. Fix what it prints,
then stop.
