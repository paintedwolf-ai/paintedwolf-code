import { stubClient } from "../test/client-fixture.ts";
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { ContributionFrameResponse } from "../api/types.ts";
import {
  contributionFrame,
  initContributionStore,
  resetContributionStoreForTest,
  ensureContributionFrame,
} from "./contribution-store.ts";

const DEN_SRC = join(dirname(fileURLToPath(import.meta.url)), "..");

function wiringFrame(): ContributionFrameResponse {
  return {
    frame_revision: "wiring",
    commands: [],
    menus: [],
    keybindings: [],
    binding_defaults: [],
    editor_actions: [],
    configuration: [],
    requirements: [],
    search_sources: [],
    operations: [],
    themes: [],
    notes: [],
  };
}

describe("contribution store wiring", () => {
  it("is initialized by the app bootstrap, with both seams", () => {
    const bootstrap = readFileSync(join(DEN_SRC, "index.tsx"), "utf8");
    const call = /initContributionStore\(([\s\S]*?)\);/.exec(bootstrap);
    expect(call, "index.tsx must call initContributionStore").not.toBeNull();
    const args = call![1]!;
    expect(args).toContain("getLycaonClient");
    // Missing frames surface through notices.
    expect(args).toContain("getRegisteredNoticeStore");
  });

  it("hydrates nothing at all without the thunk", async () => {
    resetContributionStoreForTest();
    await ensureContributionFrame();
    expect(contributionFrame()).toBeNull();
    resetContributionStoreForTest();
  });

  it("hydrates the frame once the thunk is wired", async () => {
    resetContributionStoreForTest();
    const frame = wiringFrame();
    const client = stubClient({
      getContributions: async () => frame,
    });
    initContributionStore(() => client);
    await ensureContributionFrame();
    expect(contributionFrame()?.frame_revision).toBe("wiring");
    resetContributionStoreForTest();
  });
});
