import { describe, expect, it } from "vitest";
import { trustItemTree } from "./trust-item-tree.ts";

describe("trustItemTree", () => {
  it("keeps root instructions visible and nests scoped instructions by directory", () => {
    expect(
      trustItemTree(
        [
          {
            name: "backend/internal/AGENTS.md",
            path: "backend/internal/AGENTS.md",
            root_id: "root",
          },
          { name: "AGENTS.md", path: "AGENTS.md", root_id: "root" },
          {
            name: "frontend/AGENTS.md",
            path: "frontend/AGENTS.md",
            root_id: "root",
          },
          {
            name: "backend/AGENTS.md",
            path: "backend/AGENTS.md",
            root_id: "root",
          },
        ],
        [],
      ),
    ).toEqual([
      {
        kind: "item",
        name: "AGENTS.md",
        item: { name: "AGENTS.md", path: "AGENTS.md", root_id: "root" },
        sourceIndex: 1,
      },
      {
        kind: "folder",
        name: "backend",
        path: "backend",
        children: [
          {
            kind: "item",
            name: "AGENTS.md",
            item: {
              name: "backend/AGENTS.md",
              path: "backend/AGENTS.md",
              root_id: "root",
            },
            sourceIndex: 3,
          },
          {
            kind: "folder",
            name: "internal",
            path: "backend/internal",
            children: [
              {
                kind: "item",
                name: "AGENTS.md",
                item: {
                  name: "backend/internal/AGENTS.md",
                  path: "backend/internal/AGENTS.md",
                  root_id: "root",
                },
                sourceIndex: 0,
              },
            ],
          },
        ],
      },
      {
        kind: "folder",
        name: "frontend",
        path: "frontend",
        children: [
          {
            kind: "item",
            name: "AGENTS.md",
            item: {
              name: "frontend/AGENTS.md",
              path: "frontend/AGENTS.md",
              root_id: "root",
            },
            sourceIndex: 2,
          },
        ],
      },
    ]);
  });

  it("groups duplicate paths by project root", () => {
    const tree = trustItemTree(
      [
        { name: "AGENTS.md", path: "AGENTS.md", root_id: "root-api" },
        { name: "AGENTS.md", path: "AGENTS.md", root_id: "root-web" },
      ],
      [
        { id: "root-api", label: "api" },
        { id: "root-web", label: "web" },
      ],
    );

    expect(
      tree.map((node) => [node.kind, node.name, node.kind === "folder" && node.path]),
    ).toEqual([
      ["folder", "@api", "@api"],
      ["folder", "@web", "@web"],
    ]);
  });
});
