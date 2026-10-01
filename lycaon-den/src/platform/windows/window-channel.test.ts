import { describe, expect, it } from "vitest";
import { denSourceRoot, loadSourceCorpus } from "../../test/source-corpus.ts";
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

  it("grep guard: only window-channel may call emit/emitTo/listen from @tauri-apps/api/event", () => {
    const offenders: string[] = [];
    const eventImport =
      /import\s*\(\s*["']@tauri-apps\/api\/event["']\s*\)|from\s+["']@tauri-apps\/api\/event["']/;
    const emitCall = /\b(emit|emitTo|listen)\s*\(/;
    const corpus = loadSourceCorpus(denSourceRoot, {
      extensions: [".ts", ".tsx"],
      excludeTests: true,
    });
    for (const file of corpus.files) {
      if (file.rel === "platform/windows/window-channel.ts") continue;
      const src = file.text;
      if (!eventImport.test(src)) continue;
      if (emitCall.test(src) || eventImport.test(src)) {
        // Any production import of the event module outside the channel is a miss.
        offenders.push(file.rel);
      }
    }
    expect(offenders).toEqual([]);
  });
});
