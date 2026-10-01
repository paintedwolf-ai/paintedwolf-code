---
name: review-accessibility
description: Review keyboard, focus, motion, contrast, responsive layout, and structural accessibility on a bounded route.
---

# Review accessibility

1. State the target route/screen, viewport, and checks that are possible with the available page tools. Do not claim a complete audit before inspecting the page.
2. Capture the page and inspect structural/a11y state for semantic landmarks, control names, labels, role misuse, live regions where applicable, and focusable controls.
3. Drive the keyboard path where the page exposes it: reach the principal controls, open and dismiss a modal/overlay if one is in scope, confirm focus is visible and restored, and report any unsupported interaction rather than guessing.
4. Check reflow at a 320 CSS-pixel-wide equivalent where the content type permits it, and test enlarged text up to 200% when the environment can apply it. Look for clipping, two-dimensional scrolling, unreachable controls, loss of content, or focus hidden by sticky UI. Use `measure_page` for measured geometry and `reach` for controls the pointer cannot get to, passing the live `page_open` id after driving an interaction-dependent state.
5. Check reduced-motion behavior only when the project exposes it or the environment can set it. Never claim a setting was tested when it was unavailable.
6. Measure target size and spacing for principal interactive controls when geometry is available, but distinguish a small target from controls covered by an allowed exception. Report findings with the supporting page/geometry evidence handle, relevant WCAG 2.2 criterion when useful, severity, and user impact. Separate observed failures, untested cases, and manual follow-up clearly.
7. End with: this is a bounded engineering review, not a compliance attestation; screen-reader behavior, platform settings, and assistive technology require appropriate manual testing.

See [review boundary](references/review-boundary.md) for what page state, actions, and geometry can establish versus what still needs device validation.
