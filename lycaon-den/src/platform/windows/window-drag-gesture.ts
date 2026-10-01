import { usesCustomWindowChrome } from "../runtime.ts";

/** Starts a window drag once a primary press moves past click distance. */
export function dragWindowOnMove(e: PointerEvent, onDrag: () => void) {
  if (e.button !== 0 || !usesCustomWindowChrome()) return;
  const startX = e.clientX;
  const startY = e.clientY;
  const onMove = (ev: PointerEvent) => {
    if (Math.abs(ev.clientX - startX) + Math.abs(ev.clientY - startY) < 6) {
      return;
    }
    onDrag();
    teardown();
    void import("@tauri-apps/api/window").then(({ getCurrentWindow }) =>
      getCurrentWindow().startDragging(),
    );
  };
  const teardown = () => {
    window.removeEventListener("pointermove", onMove);
    window.removeEventListener("pointerup", teardown);
    window.removeEventListener("pointercancel", teardown);
  };
  window.addEventListener("pointermove", onMove);
  window.addEventListener("pointerup", teardown);
  window.addEventListener("pointercancel", teardown);
}
