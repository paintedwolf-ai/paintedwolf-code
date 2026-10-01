# Accessibility review boundary

This skill uses existing page tools. It does not attest WCAG compliance and does not replace on-device assistive-technology testing.

## Evidence matrix

| Question | Can establish with | Cannot establish here |
|---|---|---|
| Landmarks, roles, names, labels, live regions | Structural / a11y snapshot from `capture_page` / `page_snapshot` | Whether a screen reader announces them usefully |
| Focus order and visible focus on principal controls | Keyboard `actions` + follow-up snapshot | Platform focus rings outside the page tools |
| Modal/overlay open, dismiss, focus restore | Drive + structural state when the control is reachable | OS dialogs or native menus outside the page |
| Reflow, clipping, overflow, obscured focus, hit targets | Viewport capture + `measure_page` geometry at the tested viewport/zoom | Real device zoom / system text-size bridges the tools cannot set; whether a WCAG exception applies without product context |
| Reduced motion | Only when the project or environment can apply the preference | Claiming the preference was tested when unavailable |
| Color contrast (numeric) | `measure_page` contrast facts when selectors resolve | Subjective "looks fine" from a screenshot alone |

## Severity reporting

For each finding, record:

1. Observed failure (with `page#` / `page_geometry#` handle).
2. Untested case (tool or environment could not reach it).
3. Manual follow-up (VoiceOver / Narrator / TalkBack, system text size, real device).

Name WCAG 2.2 success criteria only when the observed evidence maps to them. Reflow at 320 CSS pixels, text enlargement to 200%, Focus Not Obscured, and Target Size (Minimum) are useful checks when the tools can reproduce the required conditions, but a bounded sample cannot establish site-wide conformance. Never report that a page passes WCAG, meets WCAG, or carries any compliance attestation from this review.

## Manual follow-up (required disclosure)

Always leave these for appropriate human/device testing when the task cares about them:

- Screen-reader traversal and announcement quality
- Platform accessibility settings (text size, contrast, motion)
- Assistive technologies and input devices not available to page tools
