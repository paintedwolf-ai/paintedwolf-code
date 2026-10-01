import { describe, expect, it } from "vitest";
import { filterProjects, nextIndex } from "./project-launcher-model.ts";
import type { ProjectSummary } from "./project-summary.ts";

const summaries: ProjectSummary[] = [
  {
    id: "p1",
    displayName: "Alpha",
    folders: ["/repo/alpha"],
    primaryFolder: "/repo/alpha",
    folderLabel: "/repo/alpha",
    sessionCount: 2,
    chatCountLabel: "2 chats",
    lastActivityLabel: "1h ago",
    lastActivityAtMs: null,
    starred: false,
    isDraft: false,
    coverArtifactId: null,
    coverRootSessionId: null,
  },
  {
    id: "p2",
    displayName: "Beta",
    folders: ["/repo/beta"],
    primaryFolder: "/repo/beta",
    folderLabel: "/repo/beta",
    sessionCount: 1,
    chatCountLabel: "1 chat",
    lastActivityLabel: "new",
    lastActivityAtMs: null,
    starred: false,
    isDraft: false,
    coverArtifactId: null,
    coverRootSessionId: null,
  },
];

describe("project-launcher-model", () => {
  it("filters by name and folder path", () => {
    expect(filterProjects(summaries, "alp").map((p) => p.id)).toEqual(["p1"]);
    expect(filterProjects(summaries, "repo/beta").map((p) => p.id)).toEqual(["p2"]);
  });

  it("matches name subsequences, ranking substrings first", () => {
    // "ba" is not a substring of "Alpha" but no subsequence either; "aa" is a
    // subsequence of Alpha (A..a) and nothing in Beta.
    expect(filterProjects(summaries, "aa").map((p) => p.id)).toEqual(["p1"]);
    // Both contain "a"; Alpha has it at position 0 → substring rank ties break
    // by match position, so Beta ("a" at index 3) sorts after Alpha.
    expect(filterProjects(summaries, "a").map((p) => p.id)).toEqual(["p1", "p2"]);
  });

  it("keeps caller order (recent-first) on an empty query", () => {
    expect(filterProjects(summaries, "  ").map((p) => p.id)).toEqual(["p1", "p2"]);
  });

  it("wraps keyboard index", () => {
    expect(nextIndex(1, 2, 1)).toBe(0);
    expect(nextIndex(0, 2, -1)).toBe(1);
  });
});
