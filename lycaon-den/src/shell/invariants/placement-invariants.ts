import { readSourceText } from "../../test/stylesheet-source.ts";
import { join } from "node:path";
import { expect } from "vitest";
import {
  denSrc,
  shellSource,
  type InvariantEntry,
} from "./common.ts";

function assertPlace01(): void {
  const types = readSourceText(join(denSrc, "../shared/app-state-types.ts"));
  expect(types).toMatch(
    /export type StagePlacement = "inline" \| "split"/,
  );
  const placement = readSourceText(join(denSrc, "shell/stage-placement.ts"));
  expect(placement).toMatch(
    /export const DEFAULT_PLACEMENT: StagePlacement = "inline"/,
  );
  const shell = shellSource();
  const saves = shell.match(/saveStagePlacementMode\(/g) ?? [];
  expect(saves).toHaveLength(1);
  expect(shell).toContain("const applyStagePlacement");
  // A repeat press reads the committed placement, so the commit cannot wait on the window.
  const placementSource = readSourceText(join(denSrc, "components/shell/shell-stage-placement.ts"));
  const apply = placementSource.slice(
    placementSource.indexOf("const applyStagePlacement"),
    placementSource.indexOf("// Startup width is claimed"),
  );
  expect(apply).toContain("saveStagePlacementMode(");
  expect(apply).not.toMatch(/await claimStageChrome/);
  expect(shell).toMatch(
    /layout\.toggleSplit[\s\S]*?applyStagePlacement/,
  );
}

function assertPlace02(): void {
  const catalog = readSourceText(join(denSrc, "../shared/app-state-types.ts"));
  expect(catalog).toMatch(/CONTEXT_NAV_CATALOG/);
  const registry = readSourceText(join(denSrc, "components/stage/stage-registry.tsx"));
  expect(registry).toMatch(/export const STAGE_REGISTRY: Record<ContextNavItemId/);
  expect(registry).toContain("search:");
  expect(registry).toContain("files:");
  expect(registry).toContain("security:");
  expect(registry).toContain("artifacts:");
  expect(registry).toContain("blueprints:");
  expect(registry).toContain("extensions:");
}

function assertPlace03(): void {
  const shell = shellSource();
  expect(shell).toMatch(/openItemWindow/);
  expect(shell).toMatch(/itemWindowViews/);
}

export const PLACEMENT_INVARIANTS: InvariantEntry[] = [
  {
    id: "INV-PLACE-01",
    class: "required",
    structural: assertPlace01,
    note: "StagePlacement is exactly inline | split",
  },
  {
    id: "INV-PLACE-02",
    class: "required",
    structural: assertPlace02,
    note: "Placeable set equals CONTEXT_NAV_CATALOG; STAGE_REGISTRY is total",
  },
  {
    id: "INV-PLACE-03",
    class: "required",
    structural: assertPlace03,
    note: "Pulled-off views open via openItemWindow / itemWindowViews",
  },
];
