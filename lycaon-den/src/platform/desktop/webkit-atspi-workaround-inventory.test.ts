import { describe, expect, it } from "vitest";
import inventory from "./webkit-atspi-workaround-inventory.json";
import { denSourceRoot, loadSourceCorpus } from "../../test/source-corpus.ts";

const MODULE = "platform/desktop/webkit-atspi-workaround.ts";
const USES = /webkitAtspiWorkaroundActive\(|data-den-webkit-atspi-workaround/;

function sitesInSource(): string[] {
  const corpus = loadSourceCorpus(denSourceRoot, { extensions: [".ts", ".tsx", ".css"], excludeTests: true });
  return corpus.files
    .filter((file) => file.rel !== MODULE && !file.rel.startsWith("platform/harness/") && !file.rel.startsWith("test/") && USES.test(file.text))
    .map((file) => file.rel)
    .sort();
}

describe("WebKitGTK AT-SPI workaround inventory", () => {
  it("lists every site that diverges under the workaround, so each one is reviewed and removable", () => {
    const listed = Object.values(inventory.mechanisms).flatMap((mechanism) => mechanism.sites).sort();
    expect(
      sitesInSource(),
      "A site uses the WebKitGTK AT-SPI workaround. Name its mechanism in webkit-atspi-workaround-inventory.json.",
    ).toEqual(listed);
  });

  it("states its exit condition and a summary for each mechanism", () => {
    expect(inventory.exit).toBe("Remove when the minimum WebKitGTK includes the AT-SPI fix.");
    for (const [name, mechanism] of Object.entries(inventory.mechanisms)) {
      expect(mechanism.summary.length, name).toBeGreaterThan(0);
      expect(mechanism.sites.length, name).toBeGreaterThan(0);
    }
  });
});
