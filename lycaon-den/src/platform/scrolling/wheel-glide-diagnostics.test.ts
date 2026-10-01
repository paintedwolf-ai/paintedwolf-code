import { afterEach, describe, expect, it, vi } from "vitest";

const debug = vi.hoisted(() => ({ enabled: true, log: vi.fn() }));
const channel = vi.hoisted(() => ({
  handlers: new Map<string, (event: { payload: unknown }) => void>(),
}));

vi.mock("../../chat/stream/den-scroll-debug.ts", () => ({
  isStreamScrollDebugEnabled: () => debug.enabled,
  denScrollDebugLog: debug.log,
}));
vi.mock("../windows/window-channel.ts", () => ({
  listenHostEvent: async (event: string, handler: (event: { payload: unknown }) => void) => {
    channel.handlers.set(event, handler);
    return () => channel.handlers.delete(event);
  },
}));

import {
  WHEEL_GLIDE_EVENT,
  setupWheelGlideDiagnostics,
  type WheelGlideReport,
} from "./wheel-glide-diagnostics.ts";

afterEach(() => {
  debug.enabled = true;
  debug.log.mockReset();
  channel.handlers.clear();
});

describe("wheel glide diagnostics", () => {
  it("records each finished glide in the scroll capture", async () => {
    await setupWheelGlideDiagnostics();
    const report: WheelGlideReport = {
      requestedX: 0,
      requestedY: -40.0024,
      emittedX: 0,
      emittedY: -40,
      notches: 1,
      durationMs: 183.6,
      outcome: "landed",
    };

    channel.handlers.get(WHEEL_GLIDE_EVENT)?.({ payload: report });

    expect(debug.log).toHaveBeenCalledWith("scroll", "wheel-glide", {
      requestedX: 0,
      requestedY: -40,
      emittedX: 0,
      emittedY: -40,
      notches: 1,
      durationMs: 184,
      outcome: "landed",
    });
  });

  it("does not subscribe while scroll debugging is off", async () => {
    debug.enabled = false;

    await setupWheelGlideDiagnostics();

    expect(channel.handlers.size).toBe(0);
  });
});
