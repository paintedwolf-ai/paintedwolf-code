import { cancelScrollportFrame, scheduleScrollportFrame } from "./scrollport-frame.ts";

/** Movement below this is layout rounding, not drift. */
const DRIFT_EPSILON_PX = 0.5;

/** The macOS default double-click interval; a later press starts a new click sequence. */
const MULTI_CLICK_WINDOW_MS = 500;

/** A height change a toggle declared to the scrollport that contains it. */
export interface DeclaredHeightChange {
  /** The change reached its final height or was reversed. */
  end(): void;
}

const INERT_CHANGE: DeclaredHeightChange = { end: () => {} };

export interface PressAnchorHost {
  /** Content around the anchor moved by `delta`; carry the offset with it. */
  carry(delta: number): void;
  /** The offset sits past the painted tail, so ending the anchor would move the view. */
  holdsRange(): boolean;
  /** The anchor ended; any range it retained may settle. */
  released(): void;
}

type Anchor = {
  /** The element the reader pressed. */
  press: Element;
  /** The innermost declared shell containing the press, or the press itself. */
  subject: Element;
  /** Viewport-relative top the subject keeps. */
  top: number;
  changes: Set<object>;
  /** When the click sequence can no longer continue. */
  sequenceEndsAt: number;
};

/**
 * Keeps a pressed control in place while the height changes its click started run and
 * until that click sequence can no longer continue, so a quick second press lands on it.
 */
export class ScrollportPressAnchor {
  private activation: { target: Element; timer: ReturnType<typeof setTimeout> } | null = null;
  private anchor: Anchor | null = null;
  private sequenceTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private readonly hostEl: HTMLElement,
    private readonly viewport: HTMLElement,
    private readonly host: PressAnchorHost,
  ) {
    hostEl.addEventListener("click", this.onClick, true);
    hostEl.addEventListener("keydown", this.onKeyDown, true);
  }

  get active(): boolean {
    return this.anchor !== null;
  }

  declare(shell: HTMLElement): DeclaredHeightChange {
    const press = this.activation?.target;
    if (!press?.isConnected) return INERT_CHANGE;
    let anchor = this.anchor;
    if (!anchor || anchor.press !== press) {
      anchor = {
        press,
        subject: press,
        top: press.getBoundingClientRect().top,
        changes: new Set(),
        sequenceEndsAt: performance.now() + MULTI_CLICK_WINDOW_MS,
      };
      this.anchor = anchor;
    }
    // The toggled shell outlives re-rendered controls inside it.
    if (shell !== anchor.subject && shell.contains(press) && (anchor.subject === press || anchor.subject.contains(shell))) {
      anchor.subject = shell;
      anchor.top = shell.getBoundingClientRect().top;
    }
    const declaredOn = anchor;
    const change = {};
    declaredOn.changes.add(change);
    this.schedule();
    return {
      end: () => {
        if (!declaredOn.changes.delete(change) || declaredOn !== this.anchor) return;
        // The frame after the last change lands measures its final layout.
        if (declaredOn.changes.size === 0) this.schedule();
      },
    };
  }

  /** Layout changed while the anchor rests; content that reached the offset ends the claim. */
  noteLayout(): void {
    const anchor = this.anchor;
    if (!anchor || anchor.changes.size > 0) return;
    if (!anchor.subject.isConnected || !this.host.holdsRange()) this.end();
  }

  /** Releases the claim so the reader or the application moves the offset. */
  end(): void {
    if (!this.anchor) return;
    this.anchor = null;
    cancelScrollportFrame(this.viewport, this.frame);
    clearTimeout(this.sequenceTimer);
    this.sequenceTimer = undefined;
    this.host.released();
  }

  dispose(): void {
    this.hostEl.removeEventListener("click", this.onClick, true);
    this.hostEl.removeEventListener("keydown", this.onKeyDown, true);
    if (this.activation) clearTimeout(this.activation.timer);
    this.activation = null;
    clearTimeout(this.sequenceTimer);
    cancelScrollportFrame(this.viewport, this.frame);
    this.anchor = null;
  }

  private readonly onClick = (event: MouseEvent): void => {
    // Keyboard activation reports no click count.
    if (event.detail <= 0 || !(event.target instanceof Element)) return;
    if (this.anchor) this.anchor.sequenceEndsAt = performance.now() + MULTI_CLICK_WINDOW_MS;
    if (this.activation) clearTimeout(this.activation.timer);
    const target = event.target;
    // The next task begins after this dispatch and every microtask it queued.
    const timer = setTimeout(() => {
      if (this.activation?.target === target) this.activation = null;
    }, 0);
    this.activation = { target, timer };
  };

  private readonly onKeyDown = (): void => {
    this.end();
  };

  private schedule(): void {
    scheduleScrollportFrame(this.viewport, "measure", this.frame);
  }

  private readonly frame = (): void => {
    const anchor = this.anchor;
    if (!anchor) return;
    if (!anchor.subject.isConnected) {
      this.end();
      return;
    }
    const drift = anchor.subject.getBoundingClientRect().top - anchor.top;
    if (Math.abs(drift) >= DRIFT_EPSILON_PX) this.host.carry(drift);
    if (anchor.changes.size > 0) {
      this.schedule();
      return;
    }
    const remaining = anchor.sequenceEndsAt - performance.now();
    if (remaining <= 0 || !this.host.holdsRange()) {
      this.end();
      return;
    }
    clearTimeout(this.sequenceTimer);
    this.sequenceTimer = setTimeout(() => {
      this.sequenceTimer = undefined;
      if (this.anchor === anchor) this.schedule();
    }, remaining);
  };
}
