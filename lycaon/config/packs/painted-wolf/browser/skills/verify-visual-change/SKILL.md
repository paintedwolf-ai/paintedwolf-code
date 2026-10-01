---
name: verify-visual-change
description: Inspect the running UI and capture grounded artifacts for visible changes, requested receipts, or proposals.
---

# Verify a visual change

1. Confirm this is a page/UI task or a requested visual receipt. If it is not, do not read this skill just because browser tools exist.
2. Choose `project_dir` only for a static or built export; choose the project's own loopback `url` for bundler, development-server, SSR, authenticated, or running-app behavior. The managed browser can reach that host-local port and cannot open public hosts. Never start a generic static server to manufacture evidence.
3. For layout intent before implementation, read the `mock-before-build` skill. A render is design intent, not evidence of a running page.
4. Treat visual verification as an active smoke test: drive the affected flow via `page_act` or the project harness, requiring observable state transitions (view navigation, DOM mutation, store update) under real input. Inspect console output and structural state, and fix issues before capturing. For a one-shot claim, use `capture_page`; for stateful flows, hold the page with `page_open`, inspect with `page_snapshot`, drive with `page_act`, and close with `page_close`. A capture that is blank, broken, or inconsistent with structural evidence is a failed receipt. For multistep or async flows, use filmstrips or per-step snapshots rather than a final screenshot alone.
5. Pick the instrument by symptom:
   - Something looks wrong momentarily (flash, jump, layout shift): drive with `page_act` and `record`, adding `watch` selectors. Read the timeline summary first (visual changes, layout shift, watched jumps, stable time), then the sheet.
   - A control ignores clicks: read `reach` from `measure_page`, which names the covering or clipping element.
   - Data is missing, stale, or failing: read `network` and `errors` on the page result. Reproduce backend states with `route` fixtures instead of editing the app.
   - A user sent a screen recording: `view_video` at the moments in question.
6. For numeric layout, visibility, alignment, gap, size, position, overlap, or contrast claims, use `measure_page`. After a `page_open` / `page_act` flow, pass its page `id` so geometry is read from the exact driven state. Do not estimate geometry from a screenshot.
7. If a static capture has critical asset failures, switch to the project's running server. If live capture is unavailable, use `render_view` only as a clearly-labelled fallback and report runtime behavior as unverified. For standalone image files (.svg, raster), mockups, or visual handles, inspect them directly with `view_image`.
8. For native GUI apps (Swift, Flutter, Qt), equip the app entry point with a headless snapshot hook on `APP_SNAPSHOT`. Render the view hierarchy offscreen to that file path and terminate immediately (`NSApp.terminate(nil)` / `exit(0)`). Run with `command` using `snapshot_capture: {}` (or standalone `APP_SNAPSHOT` + `view_image`). Never use OS desktop screenshot utilities like `screencapture`, which require permission grants and capture desktop clutter. See [native app evidence](references/native-app-evidence.md).
9. Stop a background project server with its process handle when the task is complete. Put user-facing producer artifact ids in the final report's `artifact_ids`; never embed them as Markdown image URLs.

See [page evidence](references/page-evidence.md) and [native app evidence](references/native-app-evidence.md) for capture binding, evidence distinctions, and framework patterns. A screenshot alone does not prove behavior.
