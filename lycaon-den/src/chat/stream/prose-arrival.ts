import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";
import { isPresented } from "../../ui/presented.ts";

/** Delivery effects belong only to the visible task. Hidden arrivals are consumed. */
export function canPresentProse(stream: HTMLElement): boolean {
  return stream.isConnected &&
    stream.ownerDocument.visibilityState !== "hidden" &&
    !stream.closest('[data-resident="idle"]') &&
    isPresented(stream);
}

export function proseDeliveryRow(stream: HTMLElement, key: string): HTMLElement | null {
  for (const row of stream.querySelectorAll<HTMLElement>(".transcript-viewport-row")) {
    if (row.dataset.msgId === key) return row;
  }
  return null;
}

/** Opacity preserves text layout during arrival. */
export function fadeProseArrival(row: HTMLElement): void {
  const prose = row.querySelector<HTMLElement>(".assistant-prose");
  if (!prose || prefersReducedMotion(row.ownerDocument.defaultView)) return;
  const selection = row.ownerDocument.getSelection();
  if (selection && !selection.isCollapsed) return;
  prose.animate?.([{ opacity: 0.55 }, { opacity: 1 }], {
    duration: 160,
    easing: "ease-out",
  });
}

export function createProseDelivery(ports: { stream(): HTMLElement | null; following(): boolean }) {
  let frame: number | undefined;
  let rowKey: string | null = null;
  const cancel = () => {
    if (frame !== undefined) cancelAnimationFrame(frame);
    frame = undefined;
    rowKey = null;
  };
  return {
    cancel,
    pending: (key: string) => rowKey === key,
    schedule: (key: string) => {
      cancel();
      const target = ports.stream();
      if (!target) return;
      rowKey = key;
      frame = requestAnimationFrame(() => {
        frame = undefined;
        const current = rowKey;
        rowKey = null;
        if (current !== key || ports.stream() !== target || !ports.following()) return;
        if (!canPresentProse(target)) return;
        const row = proseDeliveryRow(target, key);
        if (row) fadeProseArrival(row);
      });
    },
  };
}
