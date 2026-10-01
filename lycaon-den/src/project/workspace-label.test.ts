import { describe, expect, it } from "vitest";
import { folderWorkspaceLabel, repoWorkspaceLabel } from "./workspace-label.ts";

describe("workspace labels ahead of the project record", () => {
  it("names a folder after its final segment on either separator", () => {
    expect(folderWorkspaceLabel("/Users/cd/git/lycaon")).toBe("lycaon");
    expect(folderWorkspaceLabel("/Users/cd/git/lycaon/")).toBe("lycaon");
    expect(folderWorkspaceLabel("C:\\repos\\den")).toBe("den");
  });

  it("names a clone after the URL tail without .git", () => {
    expect(repoWorkspaceLabel("https://github.com/acme/widgets.git")).toBe("widgets");
    expect(repoWorkspaceLabel("https://github.com/acme/widgets/")).toBe("widgets");
    expect(repoWorkspaceLabel("git@github.com:acme/widgets.git")).toBe("widgets");
    expect(repoWorkspaceLabel("ssh://git@host:2222/team/repo.git")).toBe("repo");
    expect(repoWorkspaceLabel("  widgets  ")).toBe("widgets");
  });
});
