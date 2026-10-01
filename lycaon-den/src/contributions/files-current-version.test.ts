import { afterEach, describe, expect, it } from "vitest";
import { contributionCommandAvailable, nativeCommandId } from "./dispatch.ts";
import { seedStockFrame } from "./stock-frame-test.ts";
import { resetContributionStoreForTest } from "./contribution-store.ts";

const COMMAND = "painted-wolf/platform:files-current-version";

afterEach(resetContributionStoreForTest);

describe("return to current version", () => {
  it("names the files.currentVersion handler", () => {
    seedStockFrame();
    expect(nativeCommandId("files.currentVersion")).toBe(COMMAND);
  });

  it("is available only while the Files stage shows a past version", () => {
    seedStockFrame({ filesStageActive: true, filesVersionHistorical: true });
    expect(contributionCommandAvailable(COMMAND)).toBe(true);

    seedStockFrame({ filesStageActive: true, filesVersionHistorical: false });
    expect(contributionCommandAvailable(COMMAND)).toBe(false);

    seedStockFrame({ filesStageActive: false, filesVersionHistorical: true });
    expect(contributionCommandAvailable(COMMAND)).toBe(false);
  });
});
