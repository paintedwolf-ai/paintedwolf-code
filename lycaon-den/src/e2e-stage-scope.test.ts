import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  CHAT_STAGE_SELECTOR,
  LIVE_CHAT_STAGE_SELECTOR,
} from "../shared/stage-selectors.ts";
import { loadSourceCorpus } from "./test/source-corpus.ts";

const src = dirname(fileURLToPath(import.meta.url));
const e2eDir = join(src, "..", "e2e");
const e2eSources = loadSourceCorpus(e2eDir, { extensions: [".ts"] });

/** Shared selectors distinguish visible chats from hidden resident copies. */
describe("chat stage scope contract", () => {
  it("Shell stamps the stage class and resident presence the selectors rely on", () => {
    const shell = readFileSync(
      join(src, "components", "shell", "Shell.tsx"),
      "utf8",
    );
    const resident = readFileSync(
      join(src, "components", "shell", "ResidentSurface.tsx"),
      "utf8",
    );
    expect(CHAT_STAGE_SELECTOR).toBe(".den-shell-stage--chat");
    expect(shell).toMatch(/den-shell-stage--chat/);
    const columns = readFileSync(
      join(src, "components", "shell", "ShellColumns.tsx"),
      "utf8",
    );
    expect(columns).toMatch(/ResidentSurface/);
    expect(resident).toMatch(/data-resident=\{presence\(\)\}/);
    expect(LIVE_CHAT_STAGE_SELECTOR).toBe(
      '.den-resident-surface:not([data-resident="idle"]) .den-shell-stage--chat',
    );
  });

  it("the harness driver and e2e helpers derive scoping from the shared selectors", () => {
    const driver = readFileSync(
      join(src, "platform", "harness", "harness-driver.ts"),
      "utf8",
    );
    const helpers = readFileSync(join(e2eDir, "helpers.ts"), "utf8");
    for (const consumer of [driver, helpers]) {
      expect(consumer).toMatch(/LIVE_CHAT_STAGE_SELECTOR/);
      expect(consumer).toMatch(/from "..*shared\/stage-selectors.ts"/);
      // No parallel hand-written copy of the selector.
      expect(consumer).not.toMatch(/\.den-shell-stage--chat/);
      expect(consumer).not.toMatch(/querySelectorAll\(CHAT_STAGE_SELECTOR\)/);
    }
  });

  it("specs never query chat-stage testids document-wide", () => {
    // Document-wide queries also match hidden resident chats.
    const banned =
      /\bpage\s*\.\s*(?:getByTestId\(\s*["'](?:chat-composer|chat-stream|composer-send)["']|locator\(\s*["'`]\[data-testid=["']?(?:chat-composer|chat-stream|composer-send))/;
    const specs = e2eSources.files.filter((file) => file.rel.endsWith(".spec.ts"));
    expect(specs.length).toBeGreaterThan(20);
    for (const file of specs) {
      expect(banned.test(file.text), `${file.rel}: unscoped chat-stage query`).toBe(
        false,
      );
    }
  });
});
