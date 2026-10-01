import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ContributionCommand } from "../api/types.ts";
import { crossbarCommands } from "../contributions/dispatch.ts";
import {
  seedStockFrame,
  stockId,
} from "../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";
import { matchActions } from "./match-actions.ts";

const SETTINGS_OPEN = stockId("settings-open");
const SESSION_STOP = stockId("session-stop");
const SESSION_NEW = stockId("session-new");
const SESSION_NEXT = stockId("session-next");
const SESSION_PREV = stockId("session-prev");
const PROJECT_NEW = stockId("project-new");
const SEARCH_OPEN = stockId("search-open");
const OVERLAY_DISMISS = stockId("overlay-dismiss");
const NAV_TOGGLE = stockId("nav-toggle");

function idsFor(query: string, recentIds?: readonly string[]): string[] {
  return matchActions(query, {
    candidates: crossbarCommands(),
    recentIds,
  }).map((c) => c.id);
}

afterEach(() => resetContributionStoreForTest());

describe("matchActions over the stock frame", () => {
  beforeEach(() => seedStockFrame());

  it("draws only from the candidates it is handed", () => {
    const candidates = crossbarCommands();
    expect(candidates.length).toBeGreaterThan(0);
    for (const row of matchActions("session", { candidates })) {
      expect(candidates.some((c) => c.id === row.id)).toBe(true);
    }
  });

  it("an empty or blank query returns every candidate", () => {
    const all = crossbarCommands()
      .map((c) => c.id)
      .sort();
    expect(idsFor("").sort()).toEqual(all);
    expect(idsFor("   ").sort()).toEqual(all);
  });

  it("matches the title substring case-insensitively", () => {
    expect(idsFor("open settings")).toContain(SETTINGS_OPEN);
  });

  it("matches authored keywords", () => {
    expect(idsFor("preferences")).toContain(SETTINGS_OPEN);
  });

  it("does not invent synonym matches beyond the authored keywords", () => {
    expect(idsFor("halt")).not.toContain(SESSION_STOP);
  });

  it("excludes commands that opt out of the palette", () => {
    const ids = idsFor("");
    expect(ids).not.toContain(SEARCH_OPEN);
    expect(ids).not.toContain(OVERLAY_DISMISS);
  });

  it("boosts recentIds when sorting", () => {
    const matched = idsFor("session", [SESSION_NEXT, SESSION_PREV]).filter(
      (id) => id.startsWith(stockId("session-")),
    );
    expect(matched[0]).toBe(SESSION_NEXT);
  });

  it("honors an explicit candidates list without treating it as the catalog", () => {
    const only = crossbarCommands().filter((c) => c.id === NAV_TOGGLE);
    expect(matchActions("sidebar", { candidates: only }).map((c) => c.id)).toEqual([
      NAV_TOGGLE,
    ]);
    expect(matchActions("settings", { candidates: only })).toEqual([]);
  });
});

describe("availability gating", () => {
  it("drops project-gated actions when no project is open", () => {
    seedStockFrame({
      projectOpen: false,
      sessionExists: false,
      sessionIdle: false,
      activityLive: false,
    });
    const ids = idsFor("");
    expect(ids).not.toContain(SESSION_NEW);
    expect(ids).not.toContain(SESSION_PREV);
    expect(ids).not.toContain(SESSION_NEXT);
  });

  it("keeps project.new reachable with and without a project", () => {
    for (const projectOpen of [true, false]) {
      seedStockFrame({ projectOpen });
      expect(idsFor("new project")).toContain(PROJECT_NEW);
      expect(idsFor("create project")).toContain(PROJECT_NEW);
    }
  });
});

describe("extension action matching", () => {
  const candidates: ContributionCommand[] = [
    {
      id: "acme/issues:create",
      provider: "acme/issues",
      title: "Create",
      category: "Issues",
      keywords: ["ticket"],
      icon: "play",
      executor: "host",
      invocation: "project",
      action_kind: "navigate",
      result_treatment: "effect",
    },
  ];

  it("matches category, provider, and keyword without letting recency bypass eligibility", () => {
    expect(matchActions("issues", { candidates }).map((row) => row.id)).toEqual(["acme/issues:create"]);
    expect(matchActions("acme/issues", { candidates }).map((row) => row.id)).toEqual(["acme/issues:create"]);
    expect(matchActions("ticket", { candidates }).map((row) => row.id)).toEqual(["acme/issues:create"]);
    expect(matchActions("unrelated", { candidates, recentIds: ["acme/issues:create"] })).toEqual([]);
  });
});

describe("multi-word queries", () => {
  const candidates: ContributionCommand[] = [
    {
      id: "foxden/tidy:tidy-review",
      provider: "foxden/tidy",
      title: "Tidy — review this change",
      category: "Tidy",
      keywords: ["tidy", "review", "cleanup"],
      icon: "play",
      executor: "host",
      invocation: "project",
      action_kind: "navigate",
      result_treatment: "effect",
    },
    {
      id: "foxden/tidy:tidy-open-files",
      provider: "foxden/tidy",
      title: "Tidy — go to Files",
      category: "Tidy",
      keywords: ["tidy", "files"],
      icon: "play",
      executor: "host",
      invocation: "project",
      action_kind: "navigate",
      result_treatment: "effect",
    },
  ];

  it("matches every term even when punctuation breaks the contiguous substring", () => {
    // Match terms across punctuation boundaries.
    expect(matchActions("tidy review", { candidates }).map((row) => row.id)).toEqual([
      "foxden/tidy:tidy-review",
    ]);
  });

  it("still matches a single word against the whole candidate set", () => {
    const ids = matchActions("tidy", { candidates }).map((row) => row.id).sort();
    expect(ids).toEqual(["foxden/tidy:tidy-open-files", "foxden/tidy:tidy-review"].sort());
  });

  it("does not match when one term is absent from every haystack", () => {
    expect(matchActions("tidy nonexistent", { candidates })).toEqual([]);
  });

  it("finds terms split across different haystacks, not just one", () => {
    // "review" only appears in tidy-review's title/keywords; "foxden" only in
    // its provider. Neither haystack alone contains both.
    expect(matchActions("foxden review", { candidates }).map((row) => row.id)).toEqual([
      "foxden/tidy:tidy-review",
    ]);
  });
});
