import { REVEAL_FLASH_MS } from "../../../ui/reveal-flash.ts";
import { DEN_SCROLLING_ATTR } from "../../../platform/scrolling/scroll-activity.ts";

const ARRIVAL_FLASH_FRAMES = {
  "0%, 70%": {
    backgroundColor:
      "color-mix(in srgb, var(--den-accent-signal) 22%, transparent)",
  },
  to: {
    backgroundColor: "transparent",
  },
};

export const sourceEditorThemeRules = {
  "&": {
    backgroundColor: "var(--den-background)",
    color: "var(--den-text)",
  },
  // Portaled tooltip containers share theme classes, but not editor geometry.
  "&.cm-editor": {
    height: "100%",
    // Paint containment would clip line widgets.
    contain: "layout style",
  },
  "&.cm-focused": {
    outline: "none",
  },
  ".cm-scroller": {
    overflow: "auto",
    // Opaque scroll paint hides virtual-line churn.
    backgroundColor: "var(--den-background)",
    // Virtual-line churn leaves scrollTop unchanged.
    overflowAnchor: "none",
    overscrollBehavior: "none",
  },
  ".cm-content": {
    caretColor: "var(--den-current-window-caret, var(--den-text))",
    padding: "12px 0 24px",
  },
  ".cm-gap": {
    // Spacer heights take effect before paint.
    transitionProperty: "none",
  },
  ".cm-line": {
    padding: "0 1.5rem 0 0.5rem",
  },
  ".cm-cursor, .cm-dropCursor": {
    borderLeftColor: "var(--den-current-window-caret, var(--den-accent))",
    borderLeftWidth: "2px",
  },
  ".cm-gutters": {
    backgroundColor: "var(--den-background)",
    color: "color-mix(in srgb, var(--den-text-muted) 72%, transparent)",
    border: "none",
    paddingLeft: "0",
  },
  ".cm-activeLineGutter": {
    backgroundColor: "transparent",
    color: "var(--den-text)",
  },
  ".cm-activeLine": {
    backgroundColor: "color-mix(in srgb, var(--den-text) 4%, transparent)",
  },
  // The caret holds steady while content moves; its blink restarts, visible first, when scrolling settles.
  [`.cm-scroller[${DEN_SCROLLING_ATTR}] > .cm-cursorLayer`]: {
    animationName: "none !important",
  },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": {
    backgroundColor: "color-mix(in srgb, var(--den-current-window-selection-strong, var(--den-selection-strong)) var(--den-editor-selection-strength), transparent) !important",
  },
  "& ::selection": {
    backgroundColor: "color-mix(in srgb, var(--den-current-window-selection-strong, var(--den-selection-strong)) var(--den-editor-selection-strength), transparent)",
  },
  ".cm-selectionMatch": {
    backgroundColor: "color-mix(in srgb, var(--den-text) 10%, transparent)",
    borderRadius: "2px",
  },
  ".cm-searchMatch": {
    backgroundColor:
      "color-mix(in srgb, var(--den-accent-signal) 28%, transparent)",
  },
  ".cm-searchMatch.cm-searchMatch-selected": {
    backgroundColor:
      "color-mix(in srgb, var(--den-accent-signal) 48%, transparent)",
  },
  ".cm-foldPlaceholder": {
    backgroundColor: "var(--den-tint-1)",
    border: "none",
    borderRadius: "var(--den-radius-sm)",
    color: "var(--den-text-muted)",
    padding: "0 6px",
    fontSize: "0.9em",
  },
  ".cm-placeholder": {
    color: "color-mix(in srgb, var(--den-text-muted) 70%, transparent)",
  },
  ".cm-den-arrival-flash": {
    animation: `cm-den-arrival-flash ${REVEAL_FLASH_MS}ms ease-out forwards`,
  },
  // Repeat landings flip data-flash so the animation runs again.
  ".cm-den-arrival-flash[data-flash='1']": {
    animationName: "cm-den-arrival-flash-alt",
  },
  ".cm-den-scope-preview": {
    backgroundColor:
      "color-mix(in srgb, var(--den-accent-signal) 22%, transparent)",
  },
  "@keyframes cm-den-arrival-flash": ARRIVAL_FLASH_FRAMES,
  "@keyframes cm-den-arrival-flash-alt": ARRIVAL_FLASH_FRAMES,
  ".cm-den-symbol-target": {
    backgroundColor:
      "color-mix(in srgb, var(--den-accent-signal) 24%, transparent)",
    borderRadius: "3px",
    boxShadow: "inset 0 -2px var(--den-accent-signal)",
  },
  // The progress underline needs a contrasting fill.
  ".cm-den-symbol-pending": {
    cursor: "progress",
    backgroundImage:
      "linear-gradient(90deg, color-mix(in srgb, var(--den-accent-signal) 22%, transparent), var(--den-accent-signal), color-mix(in srgb, var(--den-accent-signal) 22%, transparent))",
    backgroundSize: "50% 2px",
    backgroundRepeat: "no-repeat",
    backgroundPosition: "-60% 100%",
    animation: "cm-den-symbol-pending-sweep 1s linear infinite",
  },
  "@keyframes cm-den-symbol-pending-sweep": {
    from: { backgroundPosition: "-60% 100%" },
    to: { backgroundPosition: "160% 100%" },
  },
  "@media (prefers-reduced-motion: reduce)": {
    ".cm-den-arrival-flash": {
      animation: "none",
      backgroundColor:
        "color-mix(in srgb, var(--den-accent-signal) 22%, transparent)",
    },
    ".cm-den-symbol-pending": {
      animation: "none",
      backgroundImage:
        "linear-gradient(90deg, var(--den-accent-signal), var(--den-accent-signal))",
      backgroundSize: "100% 2px",
      backgroundPosition: "0 100%",
    },
  },
  ".cm-den-indent-guide": {
    borderLeft: "1px solid color-mix(in srgb, var(--den-line) 85%, transparent)",
    marginLeft: "-1px",
  },
  ".cm-den-occurrence": {
    backgroundColor:
      "color-mix(in srgb, var(--den-accent-signal) 10%, transparent)",
    boxShadow:
      "inset 0 0 0 1px color-mix(in srgb, var(--den-accent-signal) 28%, transparent)",
    borderRadius: "3px",
  },
  ".cm-highlightSpace, .cm-highlightTab": {
    backgroundImage: "none",
    color: "color-mix(in srgb, var(--den-text-muted) 55%, transparent)",
  },
  ".cm-trailingSpace": {
    backgroundColor: "color-mix(in srgb, var(--den-danger) 14%, transparent)",
  },
  ".cm-tooltip": {
    backgroundColor: "var(--den-surface-elevated)",
    border: "1px solid var(--den-line)",
    borderRadius: "var(--den-radius-sm)",
    color: "var(--den-text)",
    zIndex: "var(--den-z-anchored-surface)",
  },
  ".cm-tooltip.cm-tooltip-autocomplete": {
    boxShadow: "var(--den-shadow-md)",
    overflow: "hidden",
  },
  ".cm-tooltip-autocomplete > ul": {
    fontFamily: "var(--den-font-mono)",
    maxHeight: "16em",
  },
  ".cm-tooltip-autocomplete > ul > li": {
    padding: "2px 8px",
  },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
    backgroundColor: "var(--den-selection)",
    color: "var(--den-text)",
  },
  ".cm-completionIcon": {
    color: "var(--den-text-muted)",
  },
  ".cm-completionDetail": {
    color: "var(--den-text-muted)",
  },
  "&.cm-focused .cm-matchingBracket, .cm-matchingBracket": {
    backgroundColor: "transparent",
    outline: "1px solid color-mix(in srgb, var(--den-accent-signal) 55%, transparent)",
    borderRadius: "2px",
  },
  "&.cm-focused .cm-nonmatchingBracket, .cm-nonmatchingBracket": {
    backgroundColor: "transparent",
    outline: "1px solid color-mix(in srgb, var(--den-danger) 60%, transparent)",
    borderRadius: "2px",
  },
  ".cm-specialChar": {
    color: "var(--den-danger)",
    backgroundColor: "color-mix(in srgb, var(--den-danger) 12%, transparent)",
    borderRadius: "2px",
  },
  // Text decoration stays aligned when findings wrap.
  ".cm-lintRange": {
    backgroundImage: "none",
    paddingBottom: 0,
  },
  ".cm-den-finding": {
    textDecoration: "underline wavy",
    textDecorationThickness: "1px",
    textUnderlineOffset: "3px",
    textDecorationSkipInk: "none",
  },
  ".cm-den-finding-critical": {
    textDecorationColor: "var(--den-danger)",
  },
  ".cm-den-finding-high": {
    textDecorationColor: "var(--den-warning)",
  },
  ".cm-den-finding-medium": {
    textDecorationColor: "var(--den-status-running)",
  },
  ".cm-den-finding-low, .cm-den-finding-info": {
    textDecorationColor:
      "color-mix(in srgb, var(--den-text-muted) 80%, transparent)",
  },
  ".cm-lintPoint::after": {
    borderBottomColor: "var(--den-danger)",
  },
  ".cm-tooltip-lint": {
    padding: 0,
    maxWidth: "min(42ch, 80vw)",
  },
  // Flex order follows message, provenance, then actions.
  ".cm-diagnostic": {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: "6px",
    fontFamily: "system-ui, sans-serif",
    fontSize: "var(--text-den-hint)",
    lineHeight: 1.45,
    padding: "6px 10px",
    borderLeftWidth: "2px",
    whiteSpace: "normal",
  },
  ".cm-diagnosticText": {
    flex: "0 0 100%",
  },
  ".cm-diagnostic-error": {
    borderLeftColor: "var(--den-danger)",
  },
  ".cm-diagnostic-warning": {
    borderLeftColor: "var(--den-status-running)",
  },
  ".cm-diagnostic-info, .cm-diagnostic-hint": {
    borderLeftColor: "var(--den-line)",
  },
  ".cm-diagnosticSource": {
    flex: "0 0 100%",
    order: 1,
    fontSize: "var(--text-den-caption)",
    fontFamily: "var(--den-font-mono)",
    color: "var(--den-text-muted)",
    opacity: 1,
  },
  ".cm-diagnosticAction": {
    order: 2,
    backgroundColor: "var(--den-tint-1)",
    color: "var(--den-text)",
    border: "1px solid var(--den-line)",
    borderRadius: "var(--den-radius-sm)",
    margin: 0,
    padding: "2px 8px",
    fontSize: "var(--text-den-caption)",
    cursor: "pointer",
  },
  ".cm-diagnosticAction:hover": {
    backgroundColor: "var(--den-tint-2)",
  },
  ".cm-diagnosticAction:focus-visible": {
    outline: "1px solid var(--den-accent-signal)",
    outlineOffset: "1px",
  },
};
