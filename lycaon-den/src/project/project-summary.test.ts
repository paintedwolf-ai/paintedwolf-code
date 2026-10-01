import { describe, expect, it } from "vitest";
import { folderLabel, toProjectSummary } from "./project-summary.ts";
import { wireProject } from "../api/mocks/project-fixture.ts";

describe("project-summary", () => {
  it("labels no-folder projects", () => {
    expect(folderLabel([])).toBe("No folder");
  });

  it("labels multi-folder with primary +N", () => {
    const p = wireProject("/repo/a", "p1");
    p.roots.push({
      id: "r2",
      path: "/repo/b",
      label: "b",
      is_primary: false,
      added_at: "t",
      kind: "attached",
    });
    expect(folderLabel(p.roots)).toContain("+1");
  });

  it("maps chat count labels", () => {
    const p = wireProject("/repo/a", "p1", "");
    p.session_count = 2;
    const summary = toProjectSummary(p);
    expect(summary.displayName).toBe("Untitled");
    expect(summary.chatCountLabel).toBe("2 chats");
  });
});
