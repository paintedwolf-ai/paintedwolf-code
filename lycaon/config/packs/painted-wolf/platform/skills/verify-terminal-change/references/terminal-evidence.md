# Terminal evidence reference

Maintained sources of truth: `docs/tools.md` (Interactive exec) and `docs/grounding.md` (Surface verification). Capture built or changed terminal interfaces, including non-interactive CLI reports; routine test/build output alone needs no screen. Host approvals, confinement, secret guards, and grounding checks remain authoritative.

## Choose the runner

| Need | Runner | Evidence |
|---|---|---|
| Exit status, bounded stdout/stderr, non-interactive tests | `command` / `verify` | Command result; not a terminal-screen claim |
| One-shot terminal picture, including direct-network CLI output | `command` with `terminal_capture` | `surface_snapshot{tui}` plus optional visual `artifact_id` |
| Prompt, keyboard interaction, TTY-gated formatting, full-screen UI | `terminal_open` plus handle tools | `surface_snapshot{tui}` from the virtual screen |
| Picture of a held terminal state | `terminal_snapshot` | `surface_snapshot{tui}` plus optional visual `artifact_id` |

`terminal_capture` runs the exact one-shot process inside a sealed PTY; it is not a pipe or a replay. Piping or redirecting a TUI through ordinary `command` removes the controlling terminal and cannot establish what its screen rendered. Do not use expect-style shell scripts as a substitute for the PTY tools.

## Capture a sealed screen

1. Put the exact program argv, working directory, capability request, `terminal_capture`, size, and caption on one `command` call.
2. Use it when no input is needed and one settled screen establishes the claim. It is the required route when the action needs direct network authority, which cannot be held across turns.
3. Inspect the returned terminal state and screen. Cite the `surface_snapshot{tui}` handle and present the visual artifact when useful.
4. Do not run a weaker second invocation for the screenshot, replay saved output, or print a prior result into another PTY.

## Drive a held screen

1. Open with argv and an explicit working directory. Pin rows and columns when layout or wrapping is under review.
2. Read the initial virtual screen, cursor, dimensions, and busy state.
3. Send one bounded interaction at a time. Prefer named controls such as `Tab`, `Enter`, `Esc`, arrows, and `Ctrl-C`; send literal text only when it is non-secret.
4. Use `terminal_read` for incremental output and `terminal_snapshot` for the assertable settled screen.
5. Retain snapshots before and after a transition when ordering or focus movement matters.
6. Close the handle after the receipt is captured.

Output quiescence is the settle boundary. A fixed delay neither proves idle nor explains a timeout.

## What the snapshot establishes

The virtual screen can establish rendered cells, cursor location, dimensions, and tool-reported busy state. It cannot establish native menus or dialogs outside the PTY, assistive-technology behavior, real terminal-emulator quirks, clipboard state, or pixels beyond the rendered-grid artifact. Report those as manual follow-up.

Never send known plaintext passwords, tokens, signing material, or other secrets with `terminal_send`. If an interactive prompt is the only supported input, use an opaque `{{paintedwolf-secret:…}}` reference; otherwise stop and ask for a safer non-agent entry path.
