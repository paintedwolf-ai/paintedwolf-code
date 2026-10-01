import { describe, expect, it } from "vitest";
import { keyAt } from "../test/at.ts";
import type { WorkflowSummary } from "../api/types.ts";
import {
  blueprintLauncherPresentation,
  BLUEPRINT_LAUNCHER_PRIMARY_CAP,
  blueprintSupportingWorkflows,
  featuredWorkflows,
  launcherGlyph,
  sessionLauncherTiles,
  shouldShowSessionLauncher,
} from "./session-launcher-model.ts";

function wf(over: Partial<WorkflowSummary>): WorkflowSummary {
  return { id: "x", version: "1.0.0", name: "X", ...over };
}

const CATALOG: WorkflowSummary[] = [
  wf({ id: "security-survey", name: "Security", icon: "shield", featured: true, requires_repo: true }),
  wf({ id: "plan", name: "Plan", icon: "route", featured: true, supports_blueprints: true }),
  wf({ id: "recon-pack", name: "Recon", icon: "radar", featured: true, requires_repo: true }),
  wf({ id: "options", name: "Decide", supports_blueprints: true }),
];

describe("featuredWorkflows", () => {
  it("keeps only featured entries, ordered by name", () => {
    expect(featuredWorkflows(CATALOG).map((w) => w.id)).toEqual([
      "plan",
      "recon-pack",
      "security-survey",
    ]);
  });
});

describe("blueprintSupportingWorkflows", () => {
  it("keeps only blueprint-supporting entries, ordered by name", () => {
    expect(blueprintSupportingWorkflows(CATALOG).map((w) => w.id)).toEqual([
      "options",
      "plan",
    ]);
  });
});

describe("launcherGlyph", () => {
  it("prefers the manifest icon", () => {
    expect(launcherGlyph(wf({ icon: "shield" }))).toBe("shield");
  });
  it("falls back to a generic glyph when icon is omitted", () => {
    expect(launcherGlyph(wf({}))).toBe("workflow");
  });
});

describe("sessionLauncherTiles", () => {
  it("disables repo-oriented tiles when no repo is attached", () => {
    const tiles = sessionLauncherTiles(CATALOG, { hasRepo: false });
    const byId = Object.fromEntries(tiles.map((t) => [t.workflow.id, t]));
    expect(keyAt(byId, "plan").disabled).toBe(false);
    expect(keyAt(byId, "recon-pack").disabled).toBe(true);
    expect(keyAt(byId, "recon-pack").disabledReason).toBeTruthy();
    expect(keyAt(byId, "security-survey").disabled).toBe(true);
  });

  it("uses catalog description on tiles", () => {
    const tiles = sessionLauncherTiles(
      [
        wf({
          id: "plan",
          name: "Plan",
          featured: true,
          description: "Research, review, approve, then build.",
        }),
      ],
      { hasRepo: true },
    );
    expect(tiles[0]?.description).toBe("Research, review, approve, then build.");
  });

  it("enables repo-oriented tiles when a repo is attached", () => {
    const tiles = sessionLauncherTiles(CATALOG, { hasRepo: true });
    expect(tiles.every((t) => !t.disabled)).toBe(true);
  });
});

describe("blueprintLauncherPresentation", () => {
  it("scopes tiles to blueprint-supporting workflows only", () => {
    const { tiles, more } = blueprintLauncherPresentation(CATALOG, {
      hasRepo: true,
    });
    expect(tiles.map((t) => t.workflow.id)).toEqual(["plan", "options"]);
    expect(more).toEqual([]);
  });

  it("caps the primary strip and lists overflow for More", () => {
    const many: WorkflowSummary[] = Array.from({ length: 7 }, (_, i) =>
      wf({
        id: `bp-${i}`,
        name: `Blueprint ${i}`,
        supports_blueprints: true,
        featured: i === 2 || i === 5,
      }),
    );
    const presentation = blueprintLauncherPresentation(many, { hasRepo: true });
    expect(presentation.tiles).toHaveLength(BLUEPRINT_LAUNCHER_PRIMARY_CAP);
    expect(presentation.tiles.map((t) => t.workflow.id)).toEqual([
      "bp-2",
      "bp-5",
      "bp-0",
      "bp-1",
    ]);
    expect(presentation.more.map((t) => t.workflow.id)).toEqual([
      "bp-3",
      "bp-4",
      "bp-6",
    ]);
  });
});

describe("shouldShowSessionLauncher", () => {
  const base = {
    hasInitialPrompt: false,
    hasMessages: false,
    hasCatalogRun: false,
    streaming: false,
    tileCount: 3,
  };
  it("shows on a fresh idle session with tiles", () => {
    expect(shouldShowSessionLauncher(base)).toBe(true);
  });
  it("hides once a message exists", () => {
    expect(shouldShowSessionLauncher({ ...base, hasMessages: true })).toBe(false);
  });
  it("hides for a session opened with a Home prompt", () => {
    expect(shouldShowSessionLauncher({ ...base, hasInitialPrompt: true })).toBe(false);
  });
  it("hides during an active catalog run", () => {
    expect(shouldShowSessionLauncher({ ...base, hasCatalogRun: true })).toBe(false);
  });
  it("hides while streaming", () => {
    expect(shouldShowSessionLauncher({ ...base, streaming: true })).toBe(false);
  });
  it("hides when there are no tiles", () => {
    expect(shouldShowSessionLauncher({ ...base, tileCount: 0 })).toBe(false);
  });
});
