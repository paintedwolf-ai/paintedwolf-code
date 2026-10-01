import { describe, expect, it } from "vitest";
import type { SessionSummary } from "../api/types.ts";
import {
  SIDEBAR_CHATS_CAP,
  adjacentSession,
  compareChatsForSort,
  sidebarChatSections,
  sidebarNavigationOrder,
} from "./projects-sidebar-model.ts";

function summary(overrides: Partial<SessionSummary> = {}): SessionSummary {
  return {
    id: "s1",
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "p1",
    title: "one",
    posture: "build",
    status: "idle",
    message_count: 3,
    created_at: "2026-07-01T00:00:00Z",
    activity_at: "2026-07-01T00:00:00Z",
    ...overrides,
  };
}

const ids = (rows: { sessionId: string }[]) => rows.map((r) => r.sessionId);

describe("sidebarChatSections", () => {
  it("keeps pins in their own group, in pin order", () => {
    const sections = sidebarChatSections([
      summary({ id: "a" }),
      summary({ id: "pin-second", pin_rank: 4 }),
      summary({ id: "pin-first", pin_rank: 2 }),
    ], "created");
    expect(ids(sections.pinned)).toEqual(["pin-first", "pin-second"]);
    expect(ids(sections.chats)).toEqual(["a"]);
    expect(sections.pinned[0]?.pinRank).toBe(2);
  });

  it("orders unpinned chats newest first by creation, ignoring activity", () => {
    const sections = sidebarChatSections([
      summary({ id: "old-busy", created_at: "2026-07-01T00:00:00Z", activity_at: "2026-07-09T00:00:00Z" }),
      summary({ id: "new-quiet", created_at: "2026-07-05T00:00:00Z", activity_at: "2026-07-05T00:00:00Z" }),
    ], "created");
    expect(ids(sections.chats)).toEqual(["new-quiet", "old-busy"]);
  });

  it("orders by last activity when asked, comparing instants rather than text", () => {
    const sections = sidebarChatSections([
      summary({ id: "tenth", activity_at: "2026-07-01T00:00:05.1Z" }),
      summary({ id: "twelfth", activity_at: "2026-07-01T00:00:05.12Z" }),
    ], "activity");
    expect(ids(sections.chats)).toEqual(["twelfth", "tenth"]);
  });

  it("orders by title the way the host folds case", () => {
    const sections = sidebarChatSections([
      summary({ id: "b", title: "beta" }),
      summary({ id: "a", title: "Alpha" }),
      summary({ id: "untitled", title: "" }),
    ], "title");
    expect(ids(sections.chats)).toEqual(["untitled", "a", "b"]);
  });

  it("caps chats but never pins", () => {
    const many = Array.from({ length: 10 }, (_, i) =>
      summary({ id: `r${i}`, created_at: `2026-07-0${(i % 9) + 1}T00:00:00Z` }),
    );
    const pins = Array.from({ length: 9 }, (_, i) => summary({ id: `pin${i}`, pin_rank: i + 1 }));
    const sections = sidebarChatSections([...many, ...pins], "created");
    expect(sections.chats).toHaveLength(SIDEBAR_CHATS_CAP);
    expect(sections.pinned).toHaveLength(9);
  });

  it("lists a selected chat outside the cap last, leaving the rows above in place", () => {
    const many = Array.from({ length: 8 }, (_, i) =>
      summary({ id: `r${i}`, created_at: `2026-07-0${i + 1}T00:00:00Z` }),
    );
    const outside = many[0]!;
    const sections = sidebarChatSections(many, "created", outside);
    expect(ids(sections.chats)).toEqual(["r7", "r6", "r5", "r4", "r3", "r2", "r1", "r0"]);
    expect(ids(sidebarChatSections(many, "created", many[3]).chats)).toHaveLength(SIDEBAR_CHATS_CAP);
  });

  it("uses the shared untitled label and carries message counts", () => {
    const sections = sidebarChatSections([summary({ id: "s1", title: "  ", message_count: 0 })], "created");
    expect(sections.chats[0]?.title).toBe("Untitled chat");
    expect(sections.chats[0]?.messageCount).toBe(0);
  });
});

describe("compareChatsForSort", () => {
  it("breaks ties by id in the host's direction", () => {
    const tie = [summary({ id: "a" }), summary({ id: "b" })];
    expect(tie.slice().sort(compareChatsForSort("created")).map((s) => s.id)).toEqual(["b", "a"]);
    expect(tie.slice().sort(compareChatsForSort("title")).map((s) => s.id)).toEqual(["a", "b"]);
  });
});

describe("adjacentSession", () => {
  it("cycles pinned chats before the rest", () => {
    const order = sidebarNavigationOrder(sidebarChatSections([
      summary({ id: "chat" }),
      summary({ id: "pin", pin_rank: 1 }),
    ], "created"));
    expect(adjacentSession(order, "pin", 1)?.sessionId).toBe("chat");
    expect(adjacentSession(order, "chat", 1)?.sessionId).toBe("pin");
  });

  const sessions = [{ sessionId: "a" }, { sessionId: "b" }, { sessionId: "c" }];

  it("moves up and down with wrap", () => {
    expect(adjacentSession(sessions, "b", -1)?.sessionId).toBe("a");
    expect(adjacentSession(sessions, "b", 1)?.sessionId).toBe("c");
    expect(adjacentSession(sessions, "a", -1)?.sessionId).toBe("c");
    expect(adjacentSession(sessions, "c", 1)?.sessionId).toBe("a");
  });

  it("returns null for empty or single-session lists", () => {
    expect(adjacentSession([], "a", 1)).toBeNull();
    expect(adjacentSession([sessions[0]!], "a", 1)).toBeNull();
  });

  it("falls back to the first session when current is unknown", () => {
    expect(adjacentSession(sessions, "missing", 1)?.sessionId).toBe("a");
  });
});
