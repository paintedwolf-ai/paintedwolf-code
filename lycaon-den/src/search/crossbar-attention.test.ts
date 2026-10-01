import { describe, expect, it } from "vitest";
import type { AttentionRow } from "../api/types.ts";
import {
  CROSSBAR_IDLE_CHAT_CAP,
  CROSSBAR_NEEDS_YOU_CAP,
  blockedSessionTargets,
  buildCrossbarRows,
  orderGotoTargets,
  orderIdleChatTargets,
  type CrossbarGotoTarget,
  type CrossbarRow,
} from "./crossbar-model.ts";

const T0 = Date.parse("2026-07-27T12:00:00Z");

function attention(over: Partial<AttentionRow> = {}): AttentionRow {
  return {
    session_id: "s",
    project_id: "p",
    class: "needs_you",
    reason: "checkpoint",
    since_at: new Date(T0).toISOString(),
    ...over,
  };
}

function chat(
  sessionId: string,
  projectId: string,
  att?: AttentionRow,
): CrossbarGotoTarget {
  return {
    kind: "session",
    projectId,
    sessionId,
    label: sessionId,
    attention: att,
  };
}

function sectionsAndRows(rows: CrossbarRow[]): string[] {
  return rows.flatMap((r) => {
    if (r.kind === "section") return [`#${r.label}`];
    if (r.kind === "goto" && r.target.kind === "session") {
      return [r.target.sessionId];
    }
    return [];
  });
}

function idleRows(
  targets: CrossbarGotoTarget[],
  originProjectId: string | null = null,
): CrossbarRow[] {
  return buildCrossbarRows({
    query: "",
    mode: "everything",
    hits: [],
    includeEscalate: false,
    gotoTargets: targets,
    originProjectId,
  });
}

describe("blocked chats lead the idle box", () => {
  it("lists a chat waiting on you under Needs you, ahead of Chats", () => {
    const rows = idleRows([
      chat("recent", "p1"),
      chat("blocked", "p2", attention({ session_id: "blocked" })),
    ]);
    const order = sectionsAndRows(rows);
    expect(order[0]).toBe("#Needs you");
    expect(order[1]).toBe("blocked");
    expect(order).toContain("#Chats");
    expect(order.indexOf("#Needs you")).toBeLessThan(order.indexOf("#Chats"));
  });

  // The point of the section is the project you are not looking at.
  it("keeps a blocked chat from another project ahead of the current one", () => {
    const rows = idleRows(
      [
        chat("here", "current"),
        chat("elsewhere", "other", attention({ session_id: "elsewhere" })),
      ],
      "current",
    );
    expect(sectionsAndRows(rows)[1]).toBe("elsewhere");
  });

  it("never lists the same chat in both sections", () => {
    const rows = idleRows([chat("blocked", "p1", attention({ session_id: "blocked" }))]);
    const ids = rows.filter((r) => r.kind === "goto").map((r) => r.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(sectionsAndRows(rows).filter((x) => x === "blocked")).toHaveLength(1);
  });

  it("omits the section entirely when nothing is waiting", () => {
    const rows = idleRows([chat("a", "p1"), chat("b", "p1")]);
    expect(sectionsAndRows(rows)).not.toContain("#Needs you");
  });

  it("caps the blocked list and never leaks the overflow into Chats", () => {
    const many = Array.from({ length: CROSSBAR_NEEDS_YOU_CAP + 4 }, (_, i) =>
      chat(`s${i}`, "p1", attention({ session_id: `s${i}` })),
    );
    const rows = idleRows(many);
    const shown = rows.filter(
      (r) => r.kind === "goto" && r.target.kind === "session",
    );
    expect(shown).toHaveLength(CROSSBAR_NEEDS_YOU_CAP);
  });

  it("says how many are waiting when it cannot show them all", () => {
    const total = CROSSBAR_NEEDS_YOU_CAP + 2;
    const many = Array.from({ length: total }, (_, i) =>
      chat(`s${i}`, "p1", attention({ session_id: `s${i}` })),
    );
    const header = idleRows(many).find(
      (r) => r.kind === "section" && r.id === "section-needs-you",
    );
    expect(header?.kind === "section" && header.label).toBe(`Needs you (${total})`);
  });

  it("leaves the header plain when everything fits", () => {
    const header = idleRows([chat("a", "p1", attention({ session_id: "a" }))]).find(
      (r) => r.kind === "section" && r.id === "section-needs-you",
    );
    expect(header?.kind === "section" && header.label).toBe("Needs you");
  });

  it("shows the longest-waiting blocked chat first", () => {
    const rows = idleRows([
      chat("new", "p1", attention({ session_id: "new", since_at: new Date(T0 + 60_000).toISOString() })),
      chat("old", "p1", attention({ session_id: "old", since_at: new Date(T0).toISOString() })),
    ]);
    expect(sectionsAndRows(rows).slice(1, 3)).toEqual(["old", "new"]);
  });

  it("counts only blocked chats as blocked", () => {
    const targets = [
      chat("running", "p1", attention({ class: "running", reason: "turn_running" })),
      chat("failed", "p1", attention({ class: "error", reason: "turn_error" })),
      chat("blocked", "p1", attention({ session_id: "blocked" })),
    ];
    expect(blockedSessionTargets(targets).map((t) => t.sessionId)).toEqual(["blocked"]);
  });
});

describe("idle Chats ordering", () => {
  // The same list has to read correctly from two places.
  it("prefers the current project's chats when zoomed into one", () => {
    const ordered = orderIdleChatTargets(
      [chat("away", "other"), chat("home", "current")],
      "current",
    );
    expect(ordered.map((t) => t.sessionId)).toEqual(["home", "away"]);
  });

  it("falls back to plain most-recent order on home", () => {
    const ordered = orderIdleChatTargets(
      [chat("first", "p1"), chat("second", "p2")],
      null,
    );
    expect(ordered.map((t) => t.sessionId)).toEqual(["first", "second"]);
  });

  // A running chat in another project still beats an idle one in this project:
  // "what is happening" outranks "what is nearby".
  it("puts a live chat elsewhere ahead of an idle chat here", () => {
    const ordered = orderIdleChatTargets(
      [
        chat("idle-here", "current"),
        chat("running-away", "other", attention({ class: "running", reason: "turn_running" })),
      ],
      "current",
    );
    expect(ordered.map((t) => t.sessionId)).toEqual(["running-away", "idle-here"]);
  });

  it("respects the Chats cap", () => {
    const many = Array.from({ length: CROSSBAR_IDLE_CHAT_CAP + 3 }, (_, i) =>
      chat(`s${i}`, "p1"),
    );
    const rows = idleRows(many);
    const shown = rows.filter((r) => r.kind === "goto");
    expect(shown).toHaveLength(CROSSBAR_IDLE_CHAT_CAP);
  });
});

describe("typed go-to ordering", () => {
  it("ranks a blocked match above a running one above an idle one", () => {
    const ordered = orderGotoTargets(
      [
        chat("idle", "p1"),
        chat("running", "p1", attention({ class: "running", reason: "turn_running" })),
        chat("blocked", "p1", attention({ session_id: "blocked" })),
      ],
      null,
    );
    expect(ordered.map((t) => (t.kind === "session" ? t.sessionId : t.kind))).toEqual([
      "blocked",
      "running",
      "idle",
    ]);
  });

  it("keeps project and surface targets behind a chat that wants something", () => {
    const ordered = orderGotoTargets(
      [
        { kind: "project", projectId: "p1", label: "Project" },
        chat("blocked", "p1", attention({ session_id: "blocked" })),
      ],
      null,
    );
    expect(ordered[0]?.kind).toBe("session");
  });

  it("breaks ties toward the current project", () => {
    const ordered = orderGotoTargets(
      [chat("away", "other"), chat("home", "current")],
      "current",
    );
    expect(ordered.map((t) => (t.kind === "session" ? t.sessionId : ""))).toEqual([
      "home",
      "away",
    ]);
  });

  it("surfaces the attention row on typed matches so the chip can render", () => {
    const rows = buildCrossbarRows({
      query: "blocked",
      mode: "everything",
      hits: [],
      includeEscalate: false,
      gotoTargets: [chat("blocked", "p1", attention({ session_id: "blocked" }))],
      originProjectId: null,
    });
    const goto = rows.find((r) => r.kind === "goto");
    expect(goto?.kind === "goto" && goto.target.kind === "session" && goto.target.attention?.class).toBe(
      "needs_you",
    );
  });
});
