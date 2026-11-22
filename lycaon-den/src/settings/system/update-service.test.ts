import { afterEach, describe, expect, it, vi } from "vitest";
import { nativeUpdateService } from "./update-service.ts";

const invoke = vi.fn(async () => ({}));
const listenHostEvent = vi.fn(async () => () => {});

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invoke(...(args as [])),
}));
vi.mock("../../platform/windows/window-channel.ts", () => ({
  listenHostEvent: (...args: unknown[]) => listenHostEvent(...(args as [])),
}));

afterEach(() => {
  vi.unstubAllGlobals();
  invoke.mockClear();
  listenHostEvent.mockClear();
});

describe("native update service", () => {
  it("reaches the update feed only through native commands", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);

    await nativeUpdateService.getState();
    await nativeUpdateService.setAutomaticUpdatesEnabled(false);
    await nativeUpdateService.setChannel("preview");
    await nativeUpdateService.check();
    await nativeUpdateService.download("release-1");
    await nativeUpdateService.retry("release-1");
    await nativeUpdateService.restart("release-1");
    const stop = await nativeUpdateService.subscribe(() => {});
    stop();

    expect(fetch).not.toHaveBeenCalled();
    expect(invoke.mock.calls).toEqual([
      ["get_update_state"],
      ["set_automatic_updates_enabled", { enabled: false }],
      ["set_update_channel", { channel: "preview" }],
      ["check_update"],
      ["download_update", { expectedReleaseId: "release-1" }],
      ["retry_update", { expectedReleaseId: "release-1" }],
      ["restart_to_update", { expectedReleaseId: "release-1" }],
    ]);
    expect(listenHostEvent).toHaveBeenCalledTimes(1);
  });
});
