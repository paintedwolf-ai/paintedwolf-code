import { describe, expect, it } from "vitest";
import type { ProjectSummary } from "../project/project-summary.ts";
import {
  draftCount,
  projectsForSection,
  retainedHomeSummaries,
  sortProjectSummaries,
} from "./home-model.ts";

function summary(over: Partial<ProjectSummary> & { id: string }): ProjectSummary {
  return {
    id: over.id,
    displayName: over.displayName ?? over.id,
    folders: [],
    primaryFolder: null,
    folderLabel: "No folder",
    sessionCount: 0,
    chatCountLabel: "0 chats",
    lastActivityLabel: "new",
    lastActivityAtMs: over.lastActivityAtMs ?? null,
    starred: over.starred ?? false,
    isDraft: over.isDraft ?? false,
    coverArtifactId: over.coverArtifactId ?? null,
    coverRootSessionId: over.coverRootSessionId ?? null,
  };
}

const rows: ProjectSummary[] = [
  summary({ id: "saved" }),
  summary({ id: "starred-saved", starred: true }),
  summary({ id: "draft", isDraft: true }),
  summary({ id: "starred-draft", isDraft: true, starred: true }),
];

describe("projectsForSection", () => {
  it("keeps drafts out of saved views and honors stars across project states", () => {
    expect(projectsForSection(rows, "recents").map((r) => r.id)).toEqual([
      "saved",
      "starred-saved",
    ]);
    expect(projectsForSection(rows, "all").map((r) => r.id)).toEqual([
      "saved",
      "starred-saved",
    ]);
    expect(projectsForSection(rows, "starred").map((r) => r.id)).toEqual([
      "starred-saved",
      "starred-draft",
    ]);
  });

  it("shows only drafts in the drafts section", () => {
    expect(projectsForSection(rows, "drafts").map((r) => r.id)).toEqual([
      "draft",
      "starred-draft",
    ]);
  });
});

describe("retainedHomeSummaries", () => {
  it("keeps the shown rows while a submitted idea materializes", () => {
    const shown = [summary({ id: "saved" })];
    const withDraft = [...shown, summary({ id: "fresh-draft", isDraft: true })];
    expect(retainedHomeSummaries(shown, withDraft, true)).toBe(shown);
  });

  it("follows the registry once the shell is not materializing", () => {
    const shown = [summary({ id: "saved" })];
    const withDraft = [...shown, summary({ id: "fresh-draft", isDraft: true })];
    expect(retainedHomeSummaries(shown, withDraft, false)).toBe(withDraft);
  });

  it("paints the first rows even while materializing", () => {
    const first = [summary({ id: "saved" })];
    expect(retainedHomeSummaries(undefined, first, true)).toBe(first);
  });
});

describe("draftCount", () => {
  it("counts only draft projects", () => {
    expect(draftCount(rows)).toBe(2);
  });
});

describe("sortProjectSummaries", () => {
  it("sorts by name ascending and descending", () => {
    const list = [
      summary({ id: "b", displayName: "Beta" }),
      summary({ id: "a", displayName: "Alpha" }),
      summary({ id: "c", displayName: "Charlie" }),
    ];
    expect(sortProjectSummaries(list, "name", "asc").map((r) => r.id)).toEqual([
      "a",
      "b",
      "c",
    ]);
    expect(sortProjectSummaries(list, "name", "desc").map((r) => r.id)).toEqual([
      "c",
      "b",
      "a",
    ]);
  });

  it("sorts by updated with name as tiebreaker", () => {
    const list = [
      summary({ id: "old", displayName: "Old", lastActivityAtMs: 100 }),
      summary({ id: "new", displayName: "New", lastActivityAtMs: 300 }),
      summary({ id: "mid", displayName: "Mid", lastActivityAtMs: 200 }),
    ];
    expect(sortProjectSummaries(list, "updated", "desc").map((r) => r.id)).toEqual([
      "new",
      "mid",
      "old",
    ]);
    expect(sortProjectSummaries(list, "updated", "asc").map((r) => r.id)).toEqual([
      "old",
      "mid",
      "new",
    ]);
  });
});
