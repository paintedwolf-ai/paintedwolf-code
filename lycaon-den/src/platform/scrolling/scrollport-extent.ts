import { cancelScrollportFrame, scheduleScrollportFrame } from "./scrollport-frame.ts";
import { denScrollDebugLog } from "../../chat/stream/den-scroll-debug.ts";
import { SCROLL_EPSILON_PX, TAIL_BOUND_CONFIRM_FRAMES, SCROLLPORT_EXTENT_HOLD_ATTR, scrollportClientHeight } from "./scrollport-motion-types.ts";

type ExtentPorts = {
  isDirectInputActive(): boolean;
  isThumbGestureActive(): boolean;
  isTailPinned(): boolean;
  pressActive(): boolean;
  lastObservedOffset(): number;
  resolveTail(natural: number): number | null | undefined;
  scheduleTailBoundReconcile(attempts: number): void;
  claimsInput(event: Event): boolean;
};

export class ScrollportExtent {
  private extentHoldEl: HTMLElement | null = null;
  extentHoldAppliedPx = 0;
  private extentHoldContributionPx = 0;
  private extentHoldUnsupported = false;
  private geometryCache: Partial<Record<"scrollHeight" | "scrollWidth" | "clientHeight" | "clientWidth" | "exactClientHeight", number>> = {};
  private tailOffsetCache: number | undefined;
  private measureDepth = 0;
  private extentReclaimRequest: object | undefined;
  private lastNaturalHeight = 0;
  readonly reservedContractions = new Set<{ floor: number }>();
  private readonly extentHandlers = new Set<() => void>();
  private disposed = false;
  constructor(readonly host: HTMLElement, readonly viewport: HTMLElement, readonly content: HTMLElement, private readonly ports: ExtentPorts) {}

  subscribeExtent(handler: () => void): () => void {
    this.extentHandlers.add(handler);
    return () => {
      this.extentHandlers.delete(handler);
    };
  }

  private hasRetentionClaim(): boolean {
    return (
      this.ports.isTailPinned() ||
      this.ports.pressActive() ||
      this.reservedContractions.size > 0 ||
      this.extentHoldAppliedPx > 0 ||
      this.ports.isDirectInputActive()
    );
  }

  canRetainExtent(): boolean {
    return !this.extentHoldUnsupported;
  }

  private hostLabel(): string {
    return (
      this.host.dataset.testid ??
      `${this.host.tagName.toLowerCase()}.${this.host.classList[0] ?? ""}`
    );
  }

  refreshRetainedExtent(): void {
    // Unclaimed range requires no layout measurement.
    if (!this.canRetainExtent() || !this.hasRetentionClaim()) return;
    this.measureGeometry(() => {
      this.noteLayoutContraction(this.contentHeight());
      this.publishRetainedExtent();
    });
  }

  resizeRetainedExtent(): void {
    if (this.extentHoldAppliedPx === 0) return;
    this.refreshRetainedExtent();
  }

  private noteLayoutContraction(natural: number): void {
    const previous = this.lastNaturalHeight;
    this.lastNaturalHeight = natural;
    if (previous <= 0 || natural >= previous) return;
    const offset = Math.max(this.ports.lastObservedOffset(), this.viewport.scrollTop);
    const viewportHeight = this.geometry("clientHeight");
    if (offset + viewportHeight <= natural + SCROLL_EPSILON_PX) return;
    denScrollDebugLog("scroll", "layout-contraction", {
      host: this.hostLabel(),
      from: Math.round(previous),
      to: Math.round(natural),
      lost: Math.round(previous - natural),
      offset: Math.round(offset),
      clientHeight: viewportHeight,
      hold: Math.round(this.extentHoldAppliedPx),
      tailPinned: this.ports.isTailPinned(),
      directInput: this.ports.isDirectInputActive(),
    });
  }

  maxOffsetY(): number {
    return this.measureGeometry(() => {
      return Math.max(this.tailOffsetY(), this.viewport.scrollTop);
    });
  }

  tailOffsetY(): number {
    if (this.measureDepth > 0 && this.tailOffsetCache !== undefined) return this.tailOffsetCache;
    const naturalTail = Math.max(
      0,
      this.contentHeight() - this.exactClientHeight(),
    );
    const resolved = this.ports.resolveTail(naturalTail);
    const tail = resolved === null || resolved === undefined || !Number.isFinite(resolved)
      ? naturalTail : Math.max(0, Math.min(naturalTail, resolved));
    if (this.measureDepth > 0) this.tailOffsetCache = tail;
    return tail;
  }

  contentHeight(): number {
    return Math.max(0, this.geometry("scrollHeight") - this.extentHoldContributionPx);
  }

  inputMaxOffsetY(): number {
    return Math.max(this.viewport.scrollTop, this.tailOffsetY());
  }

  nativeMaxOffsetY(): number {
    return Math.max(0, this.geometry("scrollHeight") - this.exactClientHeight());
  }

  maxOffsetX(): number {
    return Math.max(0, this.geometry("scrollWidth") - this.geometry("clientWidth"));
  }

  publishLayoutCompensationRange(offset: number): void {
    if (!Number.isFinite(offset)) return;
    this.measureGeometry(() => this.publishRetainedExtent(offset));
  }

  geometry(key: "scrollHeight" | "scrollWidth" | "clientHeight" | "clientWidth"): number {
    if (this.measureDepth === 0) return this.viewport[key];
    return this.geometryCache[key] ??= this.viewport[key];
  }

  private exactClientHeight(): number {
    if (this.measureDepth === 0) return scrollportClientHeight(this.viewport);
    return this.geometryCache.exactClientHeight ??= scrollportClientHeight(this.viewport);
  }

  invalidateGeometry(): void {
    this.geometryCache = {};
    this.tailOffsetCache = undefined;
  }

  measureGeometry<T>(run: () => T): T {
    this.measureDepth += 1;
    try {
      return run();
    } finally {
      this.measureDepth -= 1;
      if (this.measureDepth === 0) this.invalidateGeometry();
    }
  }

  publishRetainedExtent(extraCeiling = 0): void {
    if (!this.canRetainExtent()) {
      this.clearExtentHoldSpacer();
      return;
    }
    // A reserved contraction counts as landed, so its layout cannot clamp whenever it arrives.
    let natural = this.contentHeight();
    for (const reservation of this.reservedContractions) natural = Math.min(natural, reservation.floor);
    const naturalMax = Math.max(
      0,
      natural - this.geometry("clientHeight"),
    );
    // Range past the tail is only ever an offset the reader occupies, or one a pending write targets.
    const occupied = Math.max(this.viewport.scrollTop, extraCeiling);
    let wanted = Math.max(0, Math.ceil(occupied - naturalMax));
    // Held range does not shrink during direct input to prevent paint and hit-test offset divergence.
    if (this.ports.isDirectInputActive()) wanted = Math.max(wanted, Math.round(this.extentHoldContributionPx));
    if (wanted <= 0) {
      this.clearExtentHoldSpacer();
      return;
    }
    if (this.extentHoldAppliedPx > 0 && Math.abs(this.extentHoldContributionPx - wanted) <= SCROLL_EPSILON_PX) return;
    // Range grows as max(0, B + h - clientHeight), so one write that adds range solves h exactly;
    // one that adds none sits in fill slack and retries a viewport taller.
    const clientHeight = this.geometry("clientHeight");
    let offsetPx = this.extentHoldAppliedPx > 0
      ? this.extentHoldAppliedPx - this.extentHoldContributionPx
      : 0;
    let contributed = 0;
    let height = 0;
    for (let attempt = 0; attempt < 3; attempt += 1) {
      height = Math.max(1, Math.ceil(wanted + offsetPx));
      contributed = this.writeExtentHold(height);
      // A growing viewport cannot retain blank space without changing layout.
      if (Math.abs(this.viewport.clientHeight - clientHeight) > SCROLL_EPSILON_PX) {
        this.extentHoldUnsupported = true;
        this.clearExtentHoldSpacer();
        return;
      }
      if (Math.abs(contributed - wanted) <= SCROLL_EPSILON_PX) break;
      if (contributed > SCROLL_EPSILON_PX) offsetPx = height - contributed;
      else if (attempt === 0) offsetPx += clientHeight;
      else break;
    }
    if (Math.abs(contributed - wanted) > SCROLL_EPSILON_PX && contributed < wanted) {
      this.extentHoldUnsupported = true;
      denScrollDebugLog("scroll", "extent-hold-unsupported", {
        host: this.hostLabel(),
        hold: height,
        required: wanted,
        added: contributed,
      });
      this.clearExtentHoldSpacer();
      return;
    }
    this.notifyExtentChanged();
  }

  private writeExtentHold(height: number): number {
    const natural = this.viewport.scrollHeight - this.extentHoldContributionPx;
    let hold = this.extentHoldEl;
    if (!hold) {
      hold = document.createElement("div");
      hold.setAttribute(SCROLLPORT_EXTENT_HOLD_ATTR, "");
      hold.setAttribute("aria-hidden", "true");
      hold.style.pointerEvents = "none";
      hold.style.flexShrink = "0";
      hold.style.width = "0";
      this.content.appendChild(hold);
      this.extentHoldEl = hold;
      // Blocking wheel listeners cost scroll latency, so one exists only while range is held.
      this.host.addEventListener("wheel", this.refuseInputPastTail, { passive: false });
    }
    hold.style.height = `${height}px`;
    this.extentHoldAppliedPx = height;
    this.invalidateGeometry();
    this.extentHoldContributionPx = Math.max(0, this.viewport.scrollHeight - natural);
    return this.extentHoldContributionPx;
  }

  private clearExtentHoldSpacer(): void {
    if (!this.extentHoldEl && this.extentHoldAppliedPx === 0) return;
    this.host.removeEventListener("wheel", this.refuseInputPastTail);
    this.extentHoldEl?.remove();
    this.extentHoldEl = null;
    this.extentHoldAppliedPx = 0;
    this.extentHoldContributionPx = 0;
    this.invalidateGeometry();
    this.notifyExtentChanged();
  }

  private refuseInputPastTail = (event: WheelEvent): void => {
    if (event.ctrlKey || event.deltaY <= 0 || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
    // An inner scrollport that claimed the wheel scrolls itself.
    if (!this.ports.claimsInput(event)) return;
    if (this.viewport.scrollTop >= this.tailOffsetY() - SCROLL_EPSILON_PX) event.preventDefault();
  };

  private notifyExtentChanged(): void {
    for (const handler of [...this.extentHandlers]) handler();
  }

  private reclaimExtent = (): void => {
    this.extentReclaimRequest = undefined;
    if (this.disposed || this.ports.isThumbGestureActive()) return;
    this.resizeRetainedExtent();
    this.ports.scheduleTailBoundReconcile(TAIL_BOUND_CONFIRM_FRAMES);
  };

  scheduleRetainedExtentReclaim(): void {
    if (this.disposed || this.ports.isThumbGestureActive()) return;
    this.resizeRetainedExtent();
    if (this.extentReclaimRequest) return;
    const request = this.extentReclaimRequest = {};
    queueMicrotask(() => {
      if (this.disposed || this.extentReclaimRequest !== request) return;
      scheduleScrollportFrame(this.viewport, "measure", this.reclaimExtent);
    });
  }

  private cancelRetainedExtentReclaim(): void {
    this.extentReclaimRequest = undefined;
    cancelScrollportFrame(this.viewport, this.reclaimExtent);
  }

  dispose(): void {
    this.disposed = true;
    this.extentHandlers.clear();
    this.cancelRetainedExtentReclaim();
    this.clearExtentHoldSpacer();
    this.reservedContractions.clear();
  }
}
