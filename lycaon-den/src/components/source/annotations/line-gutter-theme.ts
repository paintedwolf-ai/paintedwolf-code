import type { EditorView } from "@codemirror/view";
import { DEN_SCROLLING_ATTR } from "../../../platform/scrolling/scroll-activity.ts";

/** Hover styles wait while content scrolls under a resting pointer. */
const RESTING = `.cm-scroller:not([${DEN_SCROLLING_ATTR}])`;

export function lineGutterThemeRules(
  CELL: string,
): Parameters<typeof EditorView.baseTheme>[0] {
  return {
    [`.${CELL}`]: {
      // The spacer reserves five digits; longer source labels expand the column.
      minWidth: "calc(5ch + 18px)",
      background: "transparent",
    },
    [`.${CELL} .cm-gutterElement`]: {
      position: "relative",
      display: "flex",
      // A wrapped line's cell spans several rows; its number names the first.
      alignItems: "flex-start",
      justifyContent: "flex-end",
      paddingRight: "14px",
      paddingLeft: "2px",
      boxSizing: "border-box",
    },

    [`.${CELL}__number`]: {
      position: "relative",
      zIndex: "1",
      pointerEvents: "none",
      fontVariantNumeric: "tabular-nums",
      color: "inherit",
      background: "transparent",
      border: 0,
      padding: 0,
      margin: 0,
      font: "inherit",
      lineHeight: "inherit",
    },
    [`.${CELL}__number--foldable`]: {
      pointerEvents: "auto",
      cursor: "pointer",
      borderRadius: "2px",
      // The numeral carries the fold affordance.
      boxShadow: "inset 0 -1.5px 0 color-mix(in srgb, var(--den-text-muted) 60%, transparent)",
    },
    [`${RESTING} .${CELL}__number--foldable:hover`]: {
      color: "var(--den-text)",
      boxShadow: "inset 0 -1.5px 0 var(--den-accent)",
    },
    [`.${CELL}__number--folded`]: {
      color: "var(--den-text)",
      background: "var(--den-tint-1)",
      boxShadow: "inset 0 -1.5px 0 var(--den-accent)",
    },
    [`.${CELL}__number--foldable:focus-visible`]: {
      outline: "1px solid var(--den-accent-signal)",
      outlineOffset: "1px",
    },

    [`.${CELL}__finding`]: {
      zIndex: "1",
      position: "absolute",
      left: "2px",
      top: 0,
      bottom: 0,
      width: "14px",
      display: "flex",
      alignItems: "center",
      justifyContent: "center",
      margin: 0,
      padding: 0,
      border: 0,
      background: "transparent",
      fontSize: "var(--text-den-micro)",
      lineHeight: 1,
      cursor: "pointer",
      color: "var(--den-finding-info)",
    },
    [`.${CELL}__finding-tint`]: {
      zIndex: "1",
      position: "absolute",
      inset: 0,
      width: "100%",
      height: "100%",
      margin: 0,
      padding: 0,
      border: 0,
      background: "transparent",
      cursor: "pointer",
    },
    [`.${CELL}__finding:focus-visible, .${CELL}__finding-tint:focus-visible`]: {
      outline: "1px solid var(--den-accent-signal)",
      outlineOffset: "0",
    },

    [`.${CELL}__cell--critical .${CELL}__finding`]: { color: "var(--den-finding-critical)" },
    [`.${CELL}__cell--high .${CELL}__finding`]: { color: "var(--den-finding-high)" },
    [`.${CELL}__cell--medium .${CELL}__finding`]: { color: "var(--den-finding-medium)" },
    [`.${CELL}__cell--low .${CELL}__finding`]: { color: "var(--den-finding-low)" },
    [`.${CELL}__cell--info .${CELL}__finding`]: { color: "var(--den-finding-info)" },

    // Severity color identifies findings when no shape fits.
    [`.${CELL}__cell--tinted.${CELL}__cell--critical`]: {
      background: "var(--den-finding-critical-wash)",
      color: "var(--den-finding-critical)",
    },
    [`.${CELL}__cell--tinted.${CELL}__cell--high`]: {
      background: "var(--den-finding-high-wash)",
      color: "var(--den-finding-high)",
    },
    [`.${CELL}__cell--tinted.${CELL}__cell--medium`]: {
      background: "var(--den-finding-medium-wash)",
      color: "var(--den-finding-medium)",
    },
    [`.${CELL}__cell--tinted.${CELL}__cell--low`]: {
      background: "var(--den-finding-low-wash)",
      color: "var(--den-finding-low)",
    },
    [`.${CELL}__cell--tinted.${CELL}__cell--info`]: {
      background: "var(--den-finding-info-wash)",
      color: "var(--den-finding-info)",
    },

    [`.${CELL}__restore`]: {
      position: "absolute",
      left: "2px",
      top: "50%",
      transform: "translateY(-50%)",
      width: "21px",
      height: "16px",
      display: "flex",
      alignItems: "center",
      justifyContent: "center",
      margin: 0,
      padding: 0,
      // The chip background separates the glyph from adjacent marks.
      background: "var(--den-surface)",
      border: "1px solid var(--den-line)",
      borderRadius: "var(--den-radius-sm)",
      boxShadow: "var(--den-shadow-sm)",
      color: "var(--den-text-muted)",
      cursor: "pointer",
      zIndex: "2",
    },
    [`${RESTING} .${CELL}__restore:hover, .${CELL}__restore:focus-visible`]: {
      color: "var(--den-text)",
      borderColor: "var(--den-accent)",
      outline: "none",
    },
    [`.${CELL}__restore-glyph`]: { width: "11px", height: "11px", display: "block" },

    [`.${CELL}__cell--add::before, .${CELL}__cell--del::before`]: {
      content: '""',
      position: "absolute",
      top: 0,
      bottom: 0,
      right: "2px",
      width: "3px",
      borderRadius: "1px",
      background: "var(--den-diff-add-mark)",
    },
    [`.${CELL}__cell--del::before`]: { background: "var(--den-diff-delete-mark)" },

    [`.${CELL}__provenance`]: {
      zIndex: "1",
      position: "absolute",
      top: 0,
      bottom: 0,
      right: 0,
      width: "6px",
      margin: 0,
      padding: 0,
      border: 0,
      background: "transparent",
      cursor: "pointer",
    },
    // Linked bars widen on hover and focus without changing their diff color.
    [`${RESTING} .${CELL}__cell--linked:hover::before, .${CELL}__cell--linked:focus-within::before`]:
    {
      right: "1px",
      width: "5px",
    },
    [`.${CELL}__provenance:focus-visible`]: {
      outline: "1px solid var(--den-accent-signal)",
      outlineOffset: "1px",
      borderRadius: "2px",
    },

    [`.${CELL}__foldback`]: {
      position: "absolute",
      top: 0,
      bottom: 0,
      right: 0,
      width: "7px",
      margin: 0,
      padding: 0,
      border: 0,
      background: "transparent",
      cursor: "pointer",
    },
    // Hover widens the bar while preserving its deletion color.
    [`${RESTING} .${CELL}__cell--foldback:hover::before, .${CELL}__cell--foldback:focus-within::before`]: {
      right: "1px",
      width: "5px",
      background: "var(--den-diff-delete-hue)",
    },
    [`.${CELL}__foldback:focus-visible`]: {
      outline: "1px solid var(--den-accent-signal)",
      outlineOffset: "1px",
      borderRadius: "2px",
    },

    // The outlined boundary bar distinguishes overlapping additions.
    [`.${CELL}__cut`]: {
      position: "absolute",
      right: 0,
      top: "-7px",
      width: "16px",
      height: "14px",
      margin: 0,
      padding: 0,
      border: 0,
      background: "transparent",
      cursor: "pointer",
      zIndex: "3",
    },
    [`.${CELL}__cut::before`]: {
      content: '""',
      position: "absolute",
      right: "2px",
      top: "3px",
      width: "3px",
      height: "8px",
      borderRadius: "1.5px",
      background: "var(--den-diff-cut-mark)",
      boxShadow: "0 0 0 1.5px var(--den-background)",
      transition: "height 120ms ease, top 120ms ease, background-color 120ms ease",
    },
    [`${RESTING} .${CELL}__cut:hover::before, .${CELL}__cut[aria-expanded="true"]::before, .${CELL}__cut:focus-visible::before`]:
    {
      top: "1px",
      height: "12px",
      background: "var(--den-diff-delete-hue)",
    },
    [`.${CELL}__cut:focus-visible`]: { outline: "none" },
    [`.${CELL}__cut:focus-visible::before`]: {
      boxShadow: "0 0 0 1.5px var(--den-background), 0 0 0 3px var(--den-accent-signal)",
    },
    "@media (prefers-reduced-motion: reduce)": {
      [`.${CELL}__cut::before`]: { transition: "none" },
    },
  };
}
