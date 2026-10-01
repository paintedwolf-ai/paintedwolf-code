import { describe, expect, it } from "vitest";
import type { ProjectRoot } from "../../api/types.ts";
import { resolveSourceWorkspaceRoots } from "./source-workspace-roots.ts";

const ROOTS: ProjectRoot[] = [
  {
    id: "root-a",
    path: "/project/repo",
    label: "repo",
    is_primary: true,
    added_at: "2026-01-01T00:00:00Z",
    kind: "attached",
  },
  {
    id: "root-b",
    path: "/project/repo/packages/app",
    label: "app",
    is_primary: false,
    added_at: "2026-01-01T00:00:00Z",
    kind: "attached",
  },
];

const live = { state: "live" as const, recursive: true, unwatched_directories: 0 };

describe("resolveSourceWorkspaceRoots", () => {
  it("preserves logical metadata and applies host-resolved paths by root id", () => {
    expect(
      resolveSourceWorkspaceRoots(ROOTS, [
        { id: "root-b", path: "/checkouts/chat/packages/app", watch: live },
        { id: "root-a", path: "/checkouts/chat", watch: live },
      ]),
    ).toEqual([
      { ...ROOTS[0], path: "/checkouts/chat" },
      { ...ROOTS[1], path: "/checkouts/chat/packages/app" },
    ]);
  });

  it("preserves the host's exact physical path", () => {
    const roots = [ROOTS[0]!];
    expect(
      resolveSourceWorkspaceRoots(roots, [
        { id: "root-a", path: "/checkouts/root with trailing space ", watch: live },
      ])[0]?.path,
    ).toBe("/checkouts/root with trailing space ");
  });

  it.each([
    [[]],
    [[{ id: "root-a", path: "/checkouts/chat", watch: live }]],
    [[
      { id: "root-a", path: "/checkouts/chat", watch: live },
      { id: "root-a", path: "/checkouts/chat/again", watch: live },
    ]],
    [[
      { id: "root-a", path: "/checkouts/chat", watch: live },
      { id: "root-c", path: "/checkouts/chat/elsewhere", watch: live },
    ]],
    [[
      { id: "root-a", path: "", watch: live },
      { id: "root-b", path: "/checkouts/chat/packages/app", watch: live },
    ]],
  ])("rejects an incomplete or invalid physical root set", (workspaceRoots) => {
    expect(() => resolveSourceWorkspaceRoots(ROOTS, workspaceRoots)).toThrow(
      /workspace response/,
    );
  });
});
