import { describe, it } from "vitest";
import {
  listenHostEvent,
} from "./window-channel.ts";

describe("window-channel", () => {
  it("host event listeners are inert outside Tauri", async () => {
    const stop = await listenHostEvent("item-window-views-changed", () => {
      throw new Error("must not fire");
    });
    stop();
  });
});
