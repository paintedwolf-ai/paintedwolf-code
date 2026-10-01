import { describe, expect, it } from "vitest";
import { filesTabNames } from "./files-tab-names.ts";

describe("file tab names", () => {
  it("distinguishes duplicate names with parent suffixes across the complete open set", () => {
    const buffers = ["AGENTS.md", "lycaon-den/AGENTS.md", "packages/a/src/index.ts", "packages/b/src/index.ts", "README.md"].map(path => ({
      key: path, name: path.split("/").at(-1)!, path, rootId: "root", rootLabel: "repo",
    }));
    const names = filesTabNames(buffers);
    expect(buffers.map(buffer => names.get(buffer.key)?.qualifier)).toEqual(["repo", "lycaon-den", "a/src", "b/src", ""]);
    expect(names.get("AGENTS.md")?.label).toBe("AGENTS.md — repo");
    expect(filesTabNames([buffers[1]!]).get("lycaon-den/AGENTS.md")?.qualifier).toBe("");
  });

  it("keeps identical paths in different roots and worker views distinguishable", () => {
    const base = { name: "index.ts", path: "src/index.ts", rootLabel: "repo" };
    const names = filesTabNames([
      { ...base, key: "one", rootId: "root-one" }, { ...base, key: "two", rootId: "root-two" },
      { ...base, key: "worker", rootId: "root-one", jobId: "job-one" },
    ]);
    expect(new Set([...names.values()].map(name => name.label)).size).toBe(3);
    expect(names.get("worker")?.qualifier).toContain("Worker job-one");
  });
});
