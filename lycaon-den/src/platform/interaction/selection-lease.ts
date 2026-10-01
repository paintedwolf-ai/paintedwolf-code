/** Control selections use offsets; document selections use ranges. Both survive focus changes. */

type InputTarget = {
  el: HTMLInputElement | HTMLTextAreaElement;
  start: number;
  end: number;
  direction: "forward" | "backward" | "none";
};

export type SelectionLease = {
  /** The control that held the selection. */
  target: HTMLInputElement | HTMLTextAreaElement | null;
  /** True when the lease holds a selection worth keeping. */
  held: boolean;
  /** Reapply the selection without moving focus. */
  reapply: () => void;
  /** Restore focus and selection. */
  restore: () => void;
};

function activeInputTarget(): InputTarget | null {
  if (typeof document === "undefined") return null;
  const el = document.activeElement;
  if (!(el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement)) {
    return null;
  }
  try {
    const start = el.selectionStart;
    const end = el.selectionEnd;
    if (start === null || end === null) return null;
    return { el, start, end, direction: el.selectionDirection ?? "none" };
  } catch {
    // Some input types reject selection access.
    return null;
  }
}

/** Shadow-root ranges are not document selections. */
function livesInDocument(range: Range): boolean {
  const node = range.commonAncestorContainer;
  return node.getRootNode() === (node.ownerDocument ?? node);
}

/** Captures the current selection before focus moves. */
export function captureSelectionLease(): SelectionLease {
  const input = activeInputTarget();
  const sel = typeof window !== "undefined" ? window.getSelection() : null;
  const ranges: Range[] = [];
  // Ignore control carets mirrored into document selection.
  if (sel && !sel.isCollapsed) {
    for (let i = 0; i < sel.rangeCount; i += 1) {
      const range = sel.getRangeAt(i);
      if (livesInDocument(range)) ranges.push(range.cloneRange());
    }
  }

  const reapply = () => {
    if (input?.el.isConnected) {
      try {
        // Rewriting an unchanged range would fire a spurious select event.
        if (input.el.selectionStart !== input.start || input.el.selectionEnd !== input.end) {
          input.el.setSelectionRange(input.start, input.end, input.direction);
        }
      } catch {
        // The target may detach or reject ranges.
      }
    }
    const live = typeof window !== "undefined" ? window.getSelection() : null;
    if (!live || ranges.length === 0) return;
    // A selection the work itself made replaces the leased one; only a dropped
    // selection is restored.
    if (live.rangeCount > 0 && !live.isCollapsed) return;
    try {
      live.removeAllRanges();
      for (const range of ranges) live.addRange(range);
    } catch {
      // Saved ranges can outlive their nodes.
    }
  };

  return {
    target: input?.el ?? null,
    held: input !== null || ranges.length > 0,
    reapply,
    restore: () => {
      if (input?.el.isConnected) input.el.focus({ preventScroll: true });
      reapply();
    },
  };
}

/** Runs focus-modifying work while preserving the active document selection. */
export function preservingSelection<T>(work: () => T): T {
  const lease = captureSelectionLease();
  try {
    return work();
  } finally {
    lease.reapply();
  }
}

/** Moves focus without dropping the current selection. */
export function focusRetainingSelection(
  el: HTMLElement,
  options?: FocusOptions,
): void {
  preservingSelection(() => el.focus(options));
}
