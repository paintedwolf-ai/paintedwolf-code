[host:coordinator-progress-stale]

The user interrupted the previous run while **{{ pending }}** plan step(s) were still open, so the `## Progress` checklist may no longer match what actually happened or the user's newest message. Acting on a stale checklist repeats finished work or chases abandoned steps. Read the user's newest message, then call `update_progress` to reconcile the `## Progress` — `- [x]` for steps actually finished, `- [~]` for steps the interruption or a change of direction made moot, leaving only still-valid steps open; add rows only if the new direction needs them.
