# Page evidence reference

Source of truth for host rules: `docs/den.md` (visual evidence) and `docs/tools.md` (page tools). This file is a task recipe, not a second contract.

## Capture binding

| Binding | Use when | Do not |
|---|---|---|
| `project_dir` | Plain static HTML/CSS/JS or a built export on disk. The tree is served same-origin at `http://lycaon.capture/`, so site-absolute assets including JSON (`/config.json`) are fulfilled from disk | Point at `src/` or bare `.` for bundler-only trees; start a generic HTTP file server |
| `url` | Project's own loopback server (dev/SSR/HMR/auth/API), started with `command`/`verify` `background: true` + `process_handle`. The managed browser reaches host-local TCP | Pass remote/LAN URLs; combine with `project_dir` in one call |

Pick exactly one binding. Prefer `url` when the ask is how it looks **running**. Prefer `project_dir` for "do these built files render?" A click handler that `fetch`es a file from the same tree does not require a loopback server. `CAPTURE_URL_UNREACHABLE` after a successful request to that URL is a down server or a wrong port.

Use one-shot `capture_page` when one bounded check can establish the state. Use `page_open` followed by `page_snapshot` / `page_act` for stateful, authenticated, or exploratory flows, and close the held page with `page_close` when finished.

## Measurement binding

For an initial or static state, `measure_page` uses the same `url` / `project_dir` binding as capture. For a state reached through `page_open` and `page_act`, pass the returned live page `id` to `measure_page`. That is the only binding that preserves the driven DOM, cookies, storage, and viewport; do not reopen the page just to obtain geometry.

Selectors must resolve to one node. Take them from structural snapshot evidence, or use stable ids / `data-testid` attributes. Use a `page_snapshot` before measuring when the current live state has not yet been structurally inspected.

## Drive and capture sequence

1. Navigate with the chosen binding.
2. `wait: idle` (network-quiet and DOM-stable).
3. Read structural `state` / snapshot and console/`log` — decide from those facts.
4. Drive `actions` to the state under test; `wait: idle` again. When the next target depends on the page’s response, act once, inspect the settled `state`, then choose an enabled control. Stop on the terminal state; do not pre-script later choices. Locator precedence is testid, selector, role+(label|text), label, text. `label` and `text` match accessible name (button text, `aria-label`, labelled-by, or an associated form label). A failure returns `CAPTURE_ACTION_FAILED` with the original action index, `completed_actions`, and bounded `interactive_controls` including disabled state and text. Earlier actions remain applied; inspect the current state before retrying the failed step.
5. Keep the resulting `page#` evidence handle. The screenshot corroborates; it does not replace structural or log facts.

The capture raster comes from the protected page environment and remains visible in full. `coverage.structured` describes the targeted text-geometry screening pass; it does not downgrade canvas, image, video, or other pixels, and it is not a reason to ignore the attached raster.

## Timelines

Settled captures show where a page ends up, not what happened on the way. When the claim is about motion, a flash, a late element, or content that moves, record the drive: `page_act` with `record: {watch: [...], tail_ms: N}` (or `capture_page` with `capture: "timeline"`). The recording keeps painting frames for `tail_ms` after the last action so late effects are caught.

Read the `timeline` summary before the sheet:

- `visual_changes`, `visually_stable_at_ms`, and `still_changing_at_end` say when the page stopped changing. A page still changing at the end needs a longer `tail_ms`.
- `layout_shift.total` counts movement the user did not cause; `after_input` is movement within the browser's window after the drive's own input, which is usually expected.
- `watch[].jumps` gives each watched element's moves with their times; compare them with `actions[].start_ms`.
- `long_tasks` names main-thread stalls.

The sheet shows the start, the end, the last change, the moment after each action, and the largest changes, each labeled with its time.

## Network, errors, and route fixtures

Every page result carries `network` (failures first, then requests by time, each with status, duration, and whether a fixture, the project tree, or the network served it) and `errors` (uncaught exceptions with source and stack). A drive's result covers only what that drive caused.

To see how a page handles a backend state, answer requests with `routes` on `page_open` or `capture_page`, or change them mid-drive with a `route` action. A rule matches a URL glob (`*` wildcards; a path alone matches any origin) and optionally a method, then answers with `status`, `body` or `body_path` (a project file), `headers`, and `delay_ms`, or fails the request with `fail` (`failed`, `aborted`, `timed_out`, `connection_refused`, `connection_reset`, `name_not_resolved`, `internet_disconnected`, `blocked`). `times` limits a rule to its first N matches, so a retry can see a different answer. Fixtures reproduce states without editing the app or its server.

Each held page has its own cookies and storage, so two pages can hold two signed-in users.

## Pointer reach

`measure_page` reports `reach` for each element: `receives` (with how many of five sampled points hit it), `covered` (with the covering element), `clipped` (with the clipping ancestor), `outside_viewport`, or `not_rendered`. A `pointer_blocked` relation marks covered and clipped elements. Clicks from `page_act` aim at a point the target receives and fail when none exists, as a user's click would.

## Recordings

For a screen recording or an attached video, read the attachment's overview sheet, then use `view_video` at the moments that matter: `times_ms` for exact moments, `start_ms` and `end_ms` with `count` for a window, and `crop` to read small text. Cite the frame times you looked at.

## Filmstrip vs screenshot

| Claim | Capture mode | Cite |
|---|---|---|
| Static page, no drive script | default `capture: "screenshot"` | The single `page#` handle |
| Ordering, post-async settlement, multi-step flow | `capture: "filmstrip"` with `actions` | Per-frame `page#` (`frames[].evidence_handle` / `frame_index`) for "after step K" |
| Motion, flicker, late content, layout shift | `capture: "timeline"`, or `page_act` with `record` | The timeline summary's times and values |

N action steps produce N+1 settled frames (navigate idle + after each action). Do not cite only the final after-shot for a sequence claim.

## Fallback tree

1. `project_dir` capture with critical asset failures in `log` → restart with `url` and the project's server; do not declare victory on a degraded static shot.
2. Live capture fails (`BROWSER_UNAVAILABLE`, `CAPTURE_URL_UNREACHABLE`, …) → `render_view` mockup as a clearly labelled intent fallback. Report that runtime behavior remains unverified.
3. Do not start a generic static file server (`python -m http.server`, similar) to work around capture failure.
4. Do not descope a `Snapshot the running UI` checklist row solely because one capture attempt failed — switch binding or tool, then show something.

## Evidence kinds

| Kind | Tool | Grounds |
|---|---|---|
| `render` | `render_view` | Design intent only — never observed runtime |
| `page` / `surface_snapshot` | `capture_page` | Running-page appearance and behavior (structure + console/log required) |
| `page_geometry` | `measure_page` | Rendered state plus numeric layout/style (size, gap, align, overlap, contrast) and pointer reach |

## Presentation

- Copy producer `artifact_id` values into `artifact_ids` — on `surface_note` to show one mid-run, or on the final report at close.
- Never embed `![…](artifact-uuid)` or `<img src="artifact-uuid">` in synthesis.
- Stop a `url` server with `command_stop` + `process_handle` when the turn is done. `project_dir` leaves nothing to stop.
