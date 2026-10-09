import { readSourceText } from "../../test/stylesheet-source.ts";
import { join } from "node:path";
import { expect } from "vitest";
import {
  denSrc,
  shellSource,
  type InvariantEntry,
} from "./common.ts";

function assertSplit01(): void {
  const placement = readSourceText(join(denSrc, "shell/stage-placement.ts"));
  expect(placement).toContain("export function resolveStageColumn");
  expect(placement).toMatch(
    /export const DEFAULT_SPLIT_COMPANION: ContextNavItemId = "files"/,
  );
  const shell = shellSource();
  expect(shell).toMatch(/resolveStageColumn/);
  expect(shell).toMatch(/stageColumn\(\)\.splitLive/);
}

function assertSplit02(): void {
  const placement = readSourceText(join(denSrc, "shell/stage-placement.ts"));
  expect(placement).toMatch(/SPLIT_/);
  const store = readSourceText(join(denSrc, "shell/layout-store.ts"));
  expect(store).toMatch(/setSplitHostWidth/);
  const shell = shellSource();
  expect(shell).not.toMatch(/isSplitAvailable/);
}

function assertSplit03(): void {
  const css = readSourceText(join(denSrc, "global-components.css"));
  expect(css).toMatch(/@container den-tab-chips/);
}

function assertSplit04(): void {
  const collapse = readSourceText(join(denSrc, "shell/responsive-collapse.ts"));
  expect(collapse).toContain("export function stageOpenWindowDeficitPx");
  const dock = readSourceText(join(denSrc, "components/shell/LayoutDock.tsx"));
  const tile = dock.slice(dock.indexOf("function PlacementTile"));
  expect(tile.length).toBeGreaterThan(0);
  expect(tile).not.toMatch(/disabled/);
  const shell = shellSource();
  expect(shell).toContain("const claimStageChrome");
  expect(shell).toMatch(/widenWindowBy\(stageChromeDeficitPx\(/);
  expect(shell).toMatch(/claimStageChrome\(placement === "split"/);
  expect(shell).toMatch(/claimStageChrome\(stagePlacementMode\(\) === "split"/);
}

function assertSplit05(): void {
  const types = readSourceText(join(denSrc, "../shared/app-state-types.ts"));
  expect(types).toMatch(/chatWidthPx\?: number/);
  const store = readSourceText(join(denSrc, "shell/layout-store.ts"));
  expect(store).toMatch(/export function preferredChatWidthPx\(\)/);
  const collapse = readSourceText(join(denSrc, "shell/responsive-collapse.ts"));
  expect(collapse).toContain("export function workspaceStageFloorPx");
}

export const SPLIT_INVARIANTS: InvariantEntry[] = [
  {
    id: "INV-SPLIT-01",
    class: "required",
    structural: assertSplit01,
    note: "resolveStageColumn controls splitLive",
  },
  {
    id: "INV-SPLIT-02",
    class: "required",
    structural: assertSplit02,
    note: "Narrow host collapses visually without writing inline",
  },
  {
    id: "INV-SPLIT-03",
    class: "forbidden",
    structural: assertSplit03,
    note: "Chat tab chips collapse via container query only",
  },
  {
    id: "INV-SPLIT-04",
    class: "required",
    structural: assertSplit04,
    note: "Split is offered at any width; a narrow window is widened by the deficit",
  },
  {
    id: "INV-SPLIT-05",
    class: "required",
    structural: assertSplit05,
    note: "Split ratio is one device preference; live-split chrome uses the Files floor",
  },
];
