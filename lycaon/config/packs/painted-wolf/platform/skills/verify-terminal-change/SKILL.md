---
name: verify-terminal-change
description: Verify changed CLI/TUI output, including non-interactive reports, TTY behavior, and requested terminal images.
---

# Verify a terminal change

1. A built or changed CLI report needs a screen capture even without interaction or an explicit screenshot request. Plan a separate `Snapshot the terminal` progress row. Routine test/build output or exit-status checks alone use ordinary `command` / `verify` without capture.
2. Choose the runner from the behavior:
   - For a sealed one-shot screen, including a non-interactive CLI/TUI or an action needing one-action authority such as direct network access, call `command` with `terminal_capture: {}`. Put the exact program argv, working directory, capability request, terminal size, and caption on that one invocation.
   - For prompts, keyboard interaction, or multiple material states, call `terminal_open`, inspect its returned screen, drive it with `terminal_send`, and take `terminal_snapshot` at each material state. A held terminal cannot carry one-action direct network authority.
3. Never add a shell wrapper, replay saved output, or print a prior result to manufacture a terminal picture. The PTY must run the program and path whose behavior the receipt supports.
4. Let the PTY settle through the tool's output-quiescence boundary. Do not replace settlement with a fixed sleep. Flat or piped `command` / `verify` output is not screen evidence; a sealed `terminal_capture` and a held `terminal_snapshot` are `surface_snapshot{tui}` evidence.
5. After final edits and checks, actively exercise a representative run of the finished interface as the last validation before reporting. Inspect the screen, output, and exit status; fix any issues or formatting defects you encounter, and recapture the clean terminal screen. Report the observed screen fact with its evidence handle and copy the producer `artifact_id` into final-report `artifact_ids` so the user sees the result; never embed the id as a Markdown image URL. A failed capture leaves the Snapshot row open; disclose any remaining blocker.
6. Branch on structured reject `Code:` and correct the action. Never work around `TUI_NOT_DRIVEN`, `SURFACE_CLAIM_UNGROUNDED`, approval, confinement, or secret-input rejection by switching to a less-grounded path.
7. Close a held PTY with `terminal_close` when it is no longer needed. A sealed capture creates no terminal handle. Report unsupported keys, native dialogs, or platform behavior as untested rather than inferring them from the virtual screen.

See [terminal evidence](references/terminal-evidence.md) for binding, interaction, evidence, and limitation guidance.
