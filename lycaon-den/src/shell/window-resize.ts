import { setLayoutViewportWidth } from "./layout-store.ts";
import { sustainShellLayoutBusy } from "./shell-layout-busy.ts";

/** Marks the layout epoch and publishes viewport width once per frame. */
export function installWindowResizeEpoch(): () => void {
  let frame: number | undefined;
  const publish = () => {
    frame = undefined;
    setLayoutViewportWidth(window.innerWidth);
  };
  const onResize = () => {
    sustainShellLayoutBusy();
    if (frame !== undefined) return;
    frame = requestAnimationFrame(publish);
  };
  publish();
  window.addEventListener("resize", onResize);
  return () => {
    window.removeEventListener("resize", onResize);
    if (frame !== undefined) cancelAnimationFrame(frame);
  };
}
