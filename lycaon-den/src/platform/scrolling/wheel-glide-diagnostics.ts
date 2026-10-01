import {
  denScrollDebugLog,
  isStreamScrollDebugEnabled,
} from "../../chat/stream/den-scroll-debug.ts";
import { listenHostEvent } from "../windows/window-channel.ts";

/** Host event for one finished notched-wheel glide (macOS host). */
export const WHEEL_GLIDE_EVENT = "wheel://glide";

export type WheelGlideReport = {
  requestedX: number;
  requestedY: number;
  emittedX: number;
  emittedY: number;
  notches: number;
  durationMs: number;
  outcome: "landed" | "interrupted";
};

/** Records host wheel glides in the scroll-debug capture. */
export async function setupWheelGlideDiagnostics(): Promise<void> {
  if (!isStreamScrollDebugEnabled()) return;
  await listenHostEvent<WheelGlideReport>(WHEEL_GLIDE_EVENT, ({ payload }) => {
    denScrollDebugLog("scroll", "wheel-glide", {
      requestedX: Math.round(payload.requestedX),
      requestedY: Math.round(payload.requestedY),
      emittedX: payload.emittedX,
      emittedY: payload.emittedY,
      notches: payload.notches,
      durationMs: Math.round(payload.durationMs),
      outcome: payload.outcome,
    });
  });
}
