import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { crossbarCommands } from "../contributions/dispatch.ts";
import {
  seedStockFrame,
  stockId,
} from "../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";
import type { ContributionCommand, SearchHit } from "../api/types.ts";
import { SEARCH_RESULT_TYPES } from "./search-result-types.ts";
import { CROSSBAR_MODES } from "./crossbar-modes.ts";
import {
  CROSSBAR_ACTION_CAP,
  CROSSBAR_EVERYTHING_GROUP_ORDER,
  CROSSBAR_GOTO_CAP,
  CROSSBAR_GROUP_HIT_CAP,
  CROSSBAR_HIT_CAP,
  CROSSBAR_IDLE_CHAT_CAP,
  CROSSBAR_RECENT_CAP,
  actionableCrossbarRows,
  buildCrossbarRows,
  canEscalateCrossbar,
  cycleCrossbarMode,
  effectiveCrossbarMode,
  filterHitsForMode,
  groupHitsForEverything,
  matchGotoTargets,
  crossbarHitStatus,
  crossbarNoMatchLabel,
  crossbarServerQuery,
  shouldRunCrossbarServerSearch,
  queryLooksLikeDsl,
  crossbarArm,
  crossbarAwaitsSymbolName,
  type CrossbarGotoTarget,
} from "./crossbar-model.ts";
import { SECURITY_SCANNERS_SECTION_LABEL } from "../settings/settings-nav-model.ts";

function hit(partial: Partial<SearchHit> & Pick<SearchHit, "hit_kind" | "project_id">): SearchHit {
  return {
    hit_id: `${partial.project_id}:${partial.hit_kind}`,
    source: "test",
    ...partial,
    title: partial.title ?? partial.snippet ?? partial.hit_kind,
  };
}

describe("crossbar-model", () => {
  beforeEach(() => seedStockFrame());
  afterEach(() => resetContributionStoreForTest());

  it("cycles modes with wrap across every group tab", () => {
    const last = CROSSBAR_MODES[CROSSBAR_MODES.length - 1]!;
    expect(cycleCrossbarMode("everything", 1)).toBe("actions");
    expect(cycleCrossbarMode(last, 1)).toBe("everything");
    expect(cycleCrossbarMode("everything", -1)).toBe(last);
  });

  it("tabs through Everything, Actions, then one tab per result family", () => {
    expect([...CROSSBAR_MODES]).toEqual([
      "everything",
      "actions",
      ...SEARCH_RESULT_TYPES.map((type) => type.id),
    ]);
    expect([...CROSSBAR_MODES]).toEqual([
      "everything", "actions", "messages", "files", "symbols", "code", "evidence",
    ]);
  });

  it("emits every action across stock, authored, and uncategorized groups", () => {
    const command = (id: string, category?: string): ContributionCommand => ({
      id,
      provider: id.split(":")[0]!,
      title: "Duplicate title",
      ...(category ? { category } : {}),
      icon: "play",
      executor: "host",
      invocation: "project",
      action_kind: "navigate",
      result_treatment: "effect",
    });
    const candidates = [
      command("acme/kit:z", "Zebra"),
      command("acme/kit:navigation", "navigation"),
      command("other/kit:a", "Alpha"),
      command("other/kit:other"),
    ];
    const rows = buildCrossbarRows({
      query: "",
      mode: "actions",
      hits: [],
      includeEscalate: false,
      contributionCommands: candidates,
    });
    const emitted = rows
      .filter((row) => row.kind === "action")
      .map((row) => row.kind === "action" ? row.command.id : "");
    expect(emitted).toEqual([
      "acme/kit:navigation",
      "other/kit:a",
      "acme/kit:z",
      "other/kit:other",
    ]);
    expect(new Set(emitted)).toEqual(new Set(candidates.map((row) => row.id)));
    expect(rows.filter((row) => row.kind === "section").map((row) => row.kind === "section" ? row.label : "")).toEqual([
      "Navigation", "Alpha", "Zebra", "Actions",
    ]);
  });

  it("Files tab federates only where no local file arm exists", () => {
    // Home has no local inventory, so the Files tab federates.
    expect(shouldRunCrossbarServerSearch("foo", "files", true)).toBe(false);
    expect(shouldRunCrossbarServerSearch("foo", "files", false)).toBe(true);
    expect(shouldRunCrossbarServerSearch("path:foo", "files", true)).toBe(
      true,
    );
    expect(shouldRunCrossbarServerSearch("foo", "everything", true)).toBe(
      true,
    );
    expect(shouldRunCrossbarServerSearch("@sym", "everything", true)).toBe(
      false,
    );
    expect(shouldRunCrossbarServerSearch("foo", "actions", false)).toBe(
      false,
    );
  });

  it("suppresses the Symbols header when the @ arm has no rows", () => {
    const rows = buildCrossbarRows({
      query: "@nothing",
      mode: "everything",
      hits: [],
      includeEscalate: false,
      contributionCommands: crossbarCommands(),
      symbolRows: [],
    });
    expect(
      rows.find((r) => r.kind === "section" && r.id === "section-symbols"),
    ).toBeUndefined();
    const withRows = buildCrossbarRows({
      query: "@ni",
      mode: "everything",
      hits: [],
      includeEscalate: false,
      contributionCommands: crossbarCommands(),
      symbolRows: [
        {
          name: "nibble",
          kind: "func",
          line: 3,
          rootId: "r",
          path: "a.ts",
          bufferKey: "k",
        },
      ],
    });
    expect(
      withRows.find((r) => r.kind === "section" && r.id === "section-symbols"),
    ).toBeDefined();
  });

  it("detects fielded DSL without NL heuristics", () => {
    expect(queryLooksLikeDsl("kind:code foo")).toBe(true);
    expect(queryLooksLikeDsl("settings")).toBe(false);
  });

  it("sends the group kind token on the federated wire query", () => {
    expect(crossbarServerQuery("toolbar", "code")).toBe("kind:code toolbar");
    expect(crossbarServerQuery("kind:code toolbar", "code")).toBe(
      "kind:code toolbar",
    );
    expect(crossbarServerQuery("toolbar", "files")).toBe("kind:file toolbar");
    expect(crossbarServerQuery("toolbar", "symbols")).toBe("kind:symbol toolbar");
    expect(crossbarServerQuery("toolbar", "evidence")).toBe(
      "(kind:evidence OR kind:claim OR kind:tool OR kind:web OR kind:artifact OR kind:outcome OR kind:network) toolbar",
    );
    expect(crossbarServerQuery("toolbar", "everything")).toBe("toolbar");
    expect(crossbarServerQuery("toolbar", "actions")).toBe("toolbar");
  });

  it("each group tab controls its kinds; files and messages stand alone", () => {
    const hits = [
      hit({ hit_kind: "code", project_id: "p1", path: "a.ts", line: 4 }),
      hit({ hit_kind: "file", project_id: "p1", path: "a.ts" }),
      hit({ hit_kind: "symbol", project_id: "p1", path: "a.ts", line: 2, symbol_kind: "function" }),
      hit({ hit_kind: "message", project_id: "p1" }),
      hit({ hit_kind: "claim", project_id: "p1" }),
      hit({ hit_kind: "web", project_id: "p1" }),
      // Opt-in kind with no named group remains available in Everything.
      hit({ hit_kind: "finding", project_id: "p1" }),
    ];
    expect(filterHitsForMode(hits, "code").map((h) => h.hit_kind)).toEqual(["code"]);
    expect(filterHitsForMode(hits, "symbols").map((h) => h.hit_kind)).toEqual(["symbol"]);
    expect(filterHitsForMode(hits, "files").map((h) => h.hit_kind)).toEqual(["file"]);
    expect(filterHitsForMode(hits, "messages").map((h) => h.hit_kind)).toEqual([
      "message",
    ]);
    expect(filterHitsForMode(hits, "evidence").map((h) => h.hit_kind)).toEqual([
      "claim",
      "web",
    ]);
    expect(filterHitsForMode(hits, "everything")).toHaveLength(hits.length);
    expect(filterHitsForMode(hits, "actions")).toEqual([]);
    expect(groupHitsForEverything(hits).map((g) => [g.id, g.hits.length])).toEqual([
      ["files", 1],
      ["symbols", 1],
      ["code", 1],
      ["messages", 1],
      ["evidence", 2],
      ["other", 1],
    ]);
  });

  it("Everything sections hits by group so files cannot starve code", () => {
    expect(CROSSBAR_EVERYTHING_GROUP_ORDER).toEqual(
      expect.arrayContaining(SEARCH_RESULT_TYPES.map((type) => type.id)),
    );
    // File hits outnumber the code hits.
    const crowded = [
      ...Array.from({ length: 6 }, (_, i) =>
        hit({ hit_kind: "file", project_id: "p1", path: `a${i}.ts`, hit_id: `f${i}` }),
      ),
      ...Array.from({ length: 2 }, (_, i) =>
        hit({ hit_kind: "code", project_id: "p1", path: "a.ts", line: i + 1, hit_id: `c${i}` }),
      ),
      hit({ hit_kind: "message", project_id: "p1", hit_id: "m0" }),
    ];
    const rows = buildCrossbarRows({
      query: "a.ts",
      mode: "everything",
      hits: crowded,
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
    });
    const sections = rows.filter((r) => r.kind === "section" && r.id.startsWith("section-hits"));
    expect(sections.map((s) => (s.kind === "section" ? s.label : ""))).toEqual([
      "Files",
      "Code",
      "Messages",
    ]);
    // Only the truncated group offers its full set.
    expect(sections.map((s) => (s.kind === "section" ? s.more : undefined))).toEqual([
      "files",
      undefined,
      undefined,
    ]);
    const kinds = rows.filter((r) => r.kind === "hit").map((r) => (r.kind === "hit" ? r.hit.hit_kind : ""));
    expect(kinds.filter((k) => k === "file")).toHaveLength(CROSSBAR_GROUP_HIT_CAP);
    expect(kinds.filter((k) => k === "code")).toHaveLength(2);
    expect(kinds.filter((k) => k === "message")).toHaveLength(1);

    // Single-group tabs keep their own cap and no header.
    const filesOnly = buildCrossbarRows({
      query: "a.ts",
      mode: "files",
      hits: crowded,
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
    });
    expect(filesOnly.some((r) => r.kind === "section")).toBe(false);
    expect(filesOnly.filter((r) => r.kind === "hit")).toHaveLength(CROSSBAR_HIT_CAP);
  });

  it("Everything drops federated file hits when the local file arm lists files", () => {
    const rows = buildCrossbarRows({
      query: "readme",
      mode: "everything",
      hits: [
        hit({ hit_kind: "file", project_id: "p1", path: "README.md", hit_id: "f0" }),
        hit({ hit_kind: "code", project_id: "p1", path: "a.ts", line: 1, hit_id: "c0" }),
      ],
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
      inventoryFiles: [
        { rootId: "r", path: "README.md", basename: "README.md", dir: "", matchIndexes: [] },
      ],
    });
    const labels = rows.filter((r) => r.kind === "section").map((r) => (r.kind === "section" ? r.id : ""));
    expect(labels).toContain("section-files");
    expect(labels).toContain("section-hits-code");
    expect(labels).not.toContain("section-hits-files");
  });

  it("idle everything lists idle-home actions without escalate", () => {
    const rows = buildCrossbarRows({
      query: "",
      mode: "everything",
      hits: [],
      includeEscalate: canEscalateCrossbar("", 0),
      contributionCommands: crossbarCommands(),
    });
    const actions = actionableCrossbarRows(rows).filter((r) => r.kind === "action");
    expect(actions.length).toBeGreaterThan(0);
    expect(actions.length).toBeLessThanOrEqual(CROSSBAR_ACTION_CAP);
    expect(rows.some((r) => r.kind === "escalate")).toBe(false);
  });

  it("swaps session actions for New project when no project is open", () => {
    const actionIds = () =>
      actionableCrossbarRows(
        buildCrossbarRows({
          query: "new",
          mode: "actions",
          hits: [],
          includeEscalate: false,
          contributionCommands: crossbarCommands(),
        }),
      )
        .filter((r) => r.kind === "action")
        .map((r) => (r.kind === "action" ? r.command.id : ""));

    seedStockFrame({ projectOpen: false, sessionExists: false });
    expect(actionIds()).toContain(stockId("project-new"));
    expect(actionIds()).not.toContain(stockId("session-new"));

    seedStockFrame({ projectOpen: true });
    expect(actionIds()).toContain(stockId("session-new"));
  });

  it("gates session.stop when activity is not live", () => {
    const hasStop = () =>
      actionableCrossbarRows(
        buildCrossbarRows({
          query: "",
          mode: "actions",
          hits: [],
          includeEscalate: false,
          contributionCommands: crossbarCommands(),
        }),
      ).some((r) => r.kind === "action" && r.command.id === stockId("session-stop"));

    seedStockFrame({ activityLive: true, sessionIdle: false });
    expect(hasStop()).toBe(true);

    seedStockFrame({ activityLive: false, sessionIdle: true });
    expect(hasStop()).toBe(false);
  });

  it("caps actions and hits in everything continuum", () => {
    const manyHits = Array.from({ length: 12 }, (_, i) =>
      hit({ hit_kind: "message", project_id: "p", source_ref: `m${i}` }),
    );
    const rows = buildCrossbarRows({
      query: "open",
      mode: "everything",
      hits: manyHits,
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
    });
    const actions = actionableCrossbarRows(rows).filter((r) => r.kind === "action");
    const hits = actionableCrossbarRows(rows).filter((r) => r.kind === "hit");
    expect(actions.length).toBeLessThanOrEqual(CROSSBAR_ACTION_CAP);
    expect(hits.length).toBe(CROSSBAR_GROUP_HIT_CAP);
    expect(rows.some((r) => r.kind === "escalate")).toBe(true);
  });

  it("suppresses actions for DSL queries in everything mode", () => {
    const rows = buildCrossbarRows({
      query: "kind:code needle",
      mode: "everything",
      hits: [hit({ hit_kind: "code", project_id: "p" })],
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
    });
    expect(
      actionableCrossbarRows(rows).some((r) => r.kind === "action"),
    ).toBe(false);
    expect(actionableCrossbarRows(rows).some((r) => r.kind === "hit")).toBe(true);
  });

  it("idle home leads with capped recent queries; typing clears them", () => {
    const recents = ["kind:code auth", "retry budget", "overlay merge", "a", "b", "c"];
    const idle = buildCrossbarRows({
      query: "",
      mode: "everything",
      hits: [],
      includeEscalate: false,
      contributionCommands: crossbarCommands(),
      recentQueries: recents,
    });
    const recentRows = idle.filter((r) => r.kind === "recent");
    expect(recentRows.length).toBe(CROSSBAR_RECENT_CAP);
    expect(idle[0]).toEqual({ kind: "section", id: "section-recent", label: "Recent" });
    expect(recentRows[0]).toMatchObject({ query: "kind:code auth" });

    const typed = buildCrossbarRows({
      query: "se",
      mode: "everything",
      hits: [],
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
      recentQueries: recents,
    });
    expect(typed.some((r) => r.kind === "recent")).toBe(false);
  });

  it("keeps recents off the actions mode; shows them on idle evidence", () => {
    const args = {
      query: "",
      hits: [],
      includeEscalate: false,
      contributionCommands: crossbarCommands(),
      recentQueries: ["needle"],
    } as const;
    expect(
      buildCrossbarRows({ ...args, mode: "actions" }).some((r) => r.kind === "recent"),
    ).toBe(false);
    expect(
      buildCrossbarRows({ ...args, mode: "evidence" }).some((r) => r.kind === "recent"),
    ).toBe(true);
  });

  it("recency-boosts last-run actions on the idle home", () => {
    const rows = buildCrossbarRows({
      query: "",
      mode: "everything",
      hits: [],
      includeEscalate: false,
      contributionCommands: crossbarCommands(),
      recentActionIds: [stockId("settings-open")],
    });
    const actions = actionableCrossbarRows(rows).filter((r) => r.kind === "action");
    expect(actions[0]).toMatchObject({ command: { id: stockId("settings-open") } });
  });

  it("ignores unknown recent action ids", () => {
    const rows = buildCrossbarRows({
      query: "",
      mode: "everything",
      hits: [],
      includeEscalate: false,
      contributionCommands: crossbarCommands(),
      recentActionIds: ["not.a.command", stockId("settings-open")],
    });
    const actions = actionableCrossbarRows(rows).filter((r) => r.kind === "action");
    expect(actions[0]).toMatchObject({ command: { id: stockId("settings-open") } });
  });

  it("hit status settles empty only after search resolves", () => {
    expect(
      crossbarHitStatus({ query: "", mode: "everything", searching: false, hitCount: 0 }),
    ).toBe("idle");
    expect(
      crossbarHitStatus({ query: "x", mode: "actions", searching: true, hitCount: 0 }),
    ).toBe("idle");
    expect(
      crossbarHitStatus({ query: "x", mode: "code", searching: true, hitCount: 0 }),
    ).toBe("searching");
    expect(
      crossbarHitStatus({ query: "x", mode: "code", searching: false, hitCount: 0 }),
    ).toBe("empty");
    expect(
      crossbarHitStatus({ query: "x", mode: "everything", searching: false, hitCount: 2 }),
    ).toBe("hits");
    expect(crossbarNoMatchLabel("code")).toBe("No code matches");
    expect(crossbarNoMatchLabel("evidence")).toBe("No evidence matches");
    expect(crossbarNoMatchLabel("everything")).toBe("No matches");
  });

  it("go-to lane: idle shows recent chats first; typing matches all targets", () => {
    const targets: CrossbarGotoTarget[] = [
      { kind: "session", projectId: "p1", sessionId: "s1", label: "fix auth bug", context: "Api" },
      { kind: "session", projectId: "p1", sessionId: "s2", label: "retry budget", context: "Api" },
      { kind: "session", projectId: "p2", sessionId: "s3", label: "docs pass", context: "Site" },
      { kind: "session", projectId: "p2", sessionId: "s4", label: "old chat", context: "Site" },
      { kind: "project", projectId: "p2", label: "Site", context: "site-repo" },
      { kind: "surface", surface: "security", label: SECURITY_SCANNERS_SECTION_LABEL, context: "Api" },
    ];

    const idle = buildCrossbarRows({
      query: "",
      mode: "everything",
      hits: [],
      includeEscalate: false,
      contributionCommands: crossbarCommands(),
      gotoTargets: targets,
    });
    const idleGotos = idle.filter((r) => r.kind === "goto");
    expect(idle[0]).toEqual({ kind: "section", id: "section-goto", label: "Chats" });
    expect(idleGotos.length).toBe(CROSSBAR_IDLE_CHAT_CAP);
    expect(
      idleGotos.every((r) => r.kind === "goto" && r.target.kind === "session"),
    ).toBe(true);
    // First selectable row is the most recent chat → Mod+K, Enter jumps back.
    expect(actionableCrossbarRows(idle)[0]).toMatchObject({
      target: { sessionId: "s1" },
    });

    const typed = buildCrossbarRows({
      query: "si",
      mode: "everything",
      hits: [],
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
      gotoTargets: targets,
    });
    const typedGotos = typed.filter((r) => r.kind === "goto");
    // "si" matches Site project (label), Site-context sessions, and nothing else.
    expect(typedGotos.length).toBeGreaterThan(0);
    expect(typed.some((r) => r.kind === "section" && r.label === "Go to")).toBe(true);
    expect(
      typedGotos.some((r) => r.kind === "goto" && r.target.kind === "project"),
    ).toBe(true);
  });

  it("go-to lane stays out of non-everything modes and DSL queries", () => {
    const targets: CrossbarGotoTarget[] = [
      { kind: "project", projectId: "p", label: "kinder", context: undefined },
    ];
    for (const mode of ["actions", "evidence", "code"] as const) {
      const rows = buildCrossbarRows({
        query: "kind",
        mode,
        hits: [],
        includeEscalate: false,
        contributionCommands: crossbarCommands(),
        gotoTargets: targets,
      });
      expect(rows.some((r) => r.kind === "goto"), mode).toBe(false);
    }
    const dsl = buildCrossbarRows({
      query: "kind:code kinder",
      mode: "everything",
      hits: [],
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
      gotoTargets: targets,
    });
    expect(dsl.some((r) => r.kind === "goto")).toBe(false);
  });

  it("matchGotoTargets requires every query term across label and context", () => {
    const targets: CrossbarGotoTarget[] = Array.from({ length: 9 }, (_, i) => ({
      kind: "session",
      projectId: "p",
      sessionId: `s${i}`,
      label: `needle ${i}`,
    }));
    expect(matchGotoTargets(targets, "needle").length).toBe(9);
    expect(matchGotoTargets(targets, "NEEDLE 3").length).toBe(1);
    expect(matchGotoTargets(targets, "").length).toBe(0);
    expect(matchGotoTargets(targets, "needle nonexistent").length).toBe(0);
    // Punctuation separates search terms.
    const punctuated: CrossbarGotoTarget[] = [
      { kind: "session", projectId: "p", sessionId: "s", label: "Tidy — review this change" },
    ];
    expect(matchGotoTargets(punctuated, "tidy review").length).toBe(1);
    const rows = buildCrossbarRows({
      query: "needle",
      mode: "everything",
      hits: [],
      includeEscalate: true,
      contributionCommands: crossbarCommands(),
      gotoTargets: targets,
    });
    expect(rows.filter((r) => r.kind === "goto").length).toBe(CROSSBAR_GOTO_CAP);
  });

  it("actions mode still matches actions under DSL-looking text", () => {
    const rows = buildCrossbarRows({
      query: "settings",
      mode: "actions",
      hits: [hit({ hit_kind: "code", project_id: "p" })],
      includeEscalate: false,
      contributionCommands: crossbarCommands(),
    });
    expect(
      actionableCrossbarRows(rows).some(
        (r) => r.kind === "action" && r.command.id === stockId("settings-open"),
      ),
    ).toBe(true);
    expect(actionableCrossbarRows(rows).some((r) => r.kind === "hit")).toBe(false);
  });
});

describe("query arms", () => {
  beforeEach(() => seedStockFrame());
  afterEach(() => resetContributionStoreForTest());
  const base = {
    hits: [],
    includeEscalate: true,
    get contributionCommands() { return crossbarCommands(); },
  };

  it("reads each prefix once", () => {
    expect(crossbarArm("  > open  ")).toEqual({ kind: "actions", text: "open" });
    expect(crossbarArm("@Parse")).toEqual({ kind: "file-symbols", text: "Parse" });
    expect(crossbarArm("# ParseConfig")).toEqual({ kind: "search", text: "ParseConfig", mode: "symbols" });
    expect(crossbarArm(":42")).toEqual({ kind: "line", line: 42 });
    expect(crossbarArm(":42:5")).toEqual({ kind: "line", line: 42, column: 5 });
    expect(crossbarArm(":")).toEqual({ kind: "line", line: 0 });
    expect(crossbarArm("a.ts:42")).toEqual({ kind: "search", text: "a.ts:42" });
    expect(crossbarArm(":foo")).toEqual({ kind: "search", text: ":foo" });
  });

  it("treats > as Actions mode and matches without the prefix", () => {
    expect(effectiveCrossbarMode(">theme", "files")).toBe("actions");
    expect(shouldRunCrossbarServerSearch(">theme", "everything", false)).toBe(false);
    const rows = buildCrossbarRows({ ...base, query: ">", mode: "everything" });
    expect(rows.some((r) => r.kind === "action")).toBe(true);
    expect(rows.some((r) => r.kind === "hit" || r.kind === "inventory-file")).toBe(false);
  });

  it("jumps to a line only in the file the editor shows", () => {
    const target = { rootId: "r", path: "src/a.go" };
    expect(buildCrossbarRows({ ...base, query: ":42:5", mode: "everything", lineTarget: target }))
      .toEqual([{ kind: "line", id: "line", line: 42, column: 5, target }]);
    expect(buildCrossbarRows({ ...base, query: ":42", mode: "everything", lineTarget: null })).toEqual([]);
    expect(buildCrossbarRows({ ...base, query: ":", mode: "everything", lineTarget: target })).toEqual([]);
    expect(shouldRunCrossbarServerSearch(":42", "everything", true)).toBe(false);
  });

  it("treats # as the Symbols tab and searches its text", () => {
    expect(effectiveCrossbarMode("#parse", "everything")).toBe("symbols");
    expect(effectiveCrossbarMode("#parse", "files")).toBe("symbols");
    expect(shouldRunCrossbarServerSearch("#parse", "everything", true)).toBe(true);
    expect(crossbarServerQuery("#parse", "everything")).toBe("kind:symbol parse");
    expect(shouldRunCrossbarServerSearch("#", "everything", true)).toBe(false);
    expect(crossbarServerQuery("#", "everything")).toBe("");

    const hits = [
      hit({ hit_kind: "symbol", project_id: "p1", hit_id: "s1", title: "ParseConfig", path: "src/a.go", line: 3 }),
      hit({ hit_kind: "code", project_id: "p1", hit_id: "c1", path: "src/a.go", line: 9 }),
    ];
    const rows = buildCrossbarRows({ ...base, query: "#parse", mode: "everything", hits });
    expect(rows.filter((r) => r.kind === "hit").map((r) => (r.kind === "hit" ? r.hit.hit_id : ""))).toEqual(["s1"]);
    expect(rows.some((r) => r.kind === "action" || r.kind === "inventory-file")).toBe(false);
  });

  it("lists declarations beside files and code in Everything, with See all", () => {
    const hits = [
      hit({ hit_kind: "file", project_id: "p1", hit_id: "f1", path: "parse.go" }),
      ...Array.from({ length: CROSSBAR_GROUP_HIT_CAP + 1 }, (_, i) =>
        hit({ hit_kind: "symbol", project_id: "p1", hit_id: `s${i}`, title: `Parse${i}`, path: "parse.go", line: i + 1 }),
      ),
      hit({ hit_kind: "code", project_id: "p1", hit_id: "c1", path: "main.go", line: 4 }),
    ];
    const rows = buildCrossbarRows({ ...base, query: "Parse", mode: "everything", hits });
    const sections = rows.filter((r) => r.kind === "section" && r.id.startsWith("section-hits"));
    expect(sections.map((r) => (r.kind === "section" ? [r.label, r.more] : []))).toEqual([
      ["Files", undefined],
      ["Symbols", "symbols"],
      ["Code", undefined],
    ]);
    expect(rows.filter((r) => r.kind === "hit" && r.hit.hit_kind === "symbol")).toHaveLength(CROSSBAR_GROUP_HIT_CAP);
  });

  it("asks for a name until the Symbols tab has enough to match", () => {
    expect(crossbarAwaitsSymbolName("#", "everything")).toBe(true);
    expect(crossbarAwaitsSymbolName("#p", "everything")).toBe(true);
    expect(crossbarAwaitsSymbolName("p", "symbols")).toBe(true);
    expect(crossbarAwaitsSymbolName("pa", "symbols")).toBe(false);
    expect(crossbarAwaitsSymbolName("p", "code")).toBe(false);
    expect(shouldRunCrossbarServerSearch("#p", "everything", true)).toBe(false);
    expect(shouldRunCrossbarServerSearch("p", "symbols", true)).toBe(false);
    expect(shouldRunCrossbarServerSearch("p", "code", true)).toBe(true);
    expect(crossbarNoMatchLabel("symbols")).toBe("No symbols match");
  });

  it("offers outside paths as a project, and a reveal only where this device can", () => {
    const outside = { path: "/elsewhere/notes.md", kind: "file" as const };
    const rows = buildCrossbarRows({ ...base, query: "/elsewhere/notes.md", mode: "everything", outside, canRevealOutside: true });
    expect(rows.filter((r) => r.kind === "outside").map((r) => r.id)).toEqual(["outside:open-project", "outside:reveal"]);
    const remote = buildCrossbarRows({ ...base, query: "/elsewhere/notes.md", mode: "files", outside, canRevealOutside: false });
    expect(remote.filter((r) => r.kind === "outside").map((r) => r.id)).toEqual(["outside:open-project"]);
    const messages = buildCrossbarRows({ ...base, query: "/elsewhere/notes.md", mode: "messages", outside });
    expect(messages.some((r) => r.kind === "outside")).toBe(false);
  });

  it("lists resolved git comparisons in Everything only", () => {
    const comparison = { root_id: "r", spec: "main", kind: "branch" as const, label: "main...HEAD", before_commit: "a".repeat(40), after_commit: "b".repeat(40) };
    const rows = buildCrossbarRows({ ...base, query: "main", mode: "everything", revisions: [comparison] });
    expect(rows.filter((r) => r.kind === "revision")).toHaveLength(1);
    expect(buildCrossbarRows({ ...base, query: "main", mode: "files", revisions: [comparison] }).some((r) => r.kind === "revision")).toBe(false);
  });
});
