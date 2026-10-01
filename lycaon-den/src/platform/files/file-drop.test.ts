// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { classifyDroppedItem, DROP_REJECT_MISSING, DROP_REJECT_UNIMPORTABLE_PATH, type DroppedItem } from "./file-drop.ts";
import { relativeUnderRoot } from "../../api/project-path.ts";

const roots = [
  { id: "root-a", path: "/proj", is_primary: true },
] as const;

describe("classifyDroppedItem", () => {
  it("web File → web-file", () => {
    const file = new File(["hi"], "note.txt", { type: "text/plain" });
    const item: DroppedItem = { source: "file", file };
    expect(classifyDroppedItem(item, "proj-1", roots, null, "linux")).toEqual({
      kind: "web-file",
      file,
    });
  });

  it("in-root folder → path-folder", () => {
    const item: DroppedItem = {
      source: "path",
      absolutePath: "/proj/src",
    };
    expect(
      classifyDroppedItem(item, "proj-1", roots, "folder", "linux"),
    ).toEqual({
      kind: "path-folder",
      projectId: "proj-1",
      rootId: "root-a",
      path: "src",
    });
  });

  it("in-root non-image file → path-file", () => {
    const item: DroppedItem = {
      source: "path",
      absolutePath: "/proj/src/main.go",
    };
    expect(classifyDroppedItem(item, "proj-1", roots, "file", "linux")).toEqual(
      {
        kind: "path-file",
        projectId: "proj-1",
        rootId: "root-a",
        path: "src/main.go",
      },
    );
  });

  it("in-root image → bytes-media", () => {
    const item: DroppedItem = {
      source: "path",
      absolutePath: "/proj/assets/shot.png",
    };
    expect(classifyDroppedItem(item, "proj-1", roots, "file", "linux")).toEqual(
      {
        kind: "bytes-media",
        absolutePath: "/proj/assets/shot.png",
        rootId: "root-a",
      },
    );
  });

  it("in-root screen recording → bytes-media, so its frames reach the model", () => {
    const item: DroppedItem = { source: "path", absolutePath: "/proj/recordings/bug.mov" };
    expect(classifyDroppedItem(item, "proj-1", roots, "file", "linux")).toEqual({
      kind: "bytes-media",
      absolutePath: "/proj/recordings/bug.mov",
      rootId: "root-a",
    });
  });

  it("out-of-root regular file → immutable external-file import", () => {
    const item: DroppedItem = {
      source: "path",
      absolutePath: "/other/secret.png",
    };
    expect(classifyDroppedItem(item, "proj-1", roots, "file", "linux")).toEqual(
      {
        kind: "external-file",
        absolutePath: "/other/secret.png",
      },
    );
  });

  it("out-of-root folder → reject", () => {
    const item: DroppedItem = {
      source: "path",
      absolutePath: "/tmp/folder",
    };
    expect(
      classifyDroppedItem(item, "proj-1", roots, "folder", "linux"),
    ).toEqual({
      kind: "reject",
      reason: DROP_REJECT_UNIMPORTABLE_PATH,
    });
  });

  it("missing under root → reject", () => {
    const item: DroppedItem = {
      source: "path",
      absolutePath: "/proj/gone.txt",
    };
    expect(
      classifyDroppedItem(item, "proj-1", roots, "missing", "linux"),
    ).toEqual({
      kind: "reject",
      reason: DROP_REJECT_MISSING,
    });
  });

  it("sibling-prefix files are external imports, never project references", () => {
    const item: DroppedItem = {
      source: "path",
      absolutePath: "/proj-evil/a.txt",
    };
    expect(classifyDroppedItem(item, "proj-1", roots, "file", "linux")).toEqual(
      {
        kind: "external-file",
        absolutePath: "/proj-evil/a.txt",
      },
    );
  });
});

describe("relativeUnderRoot", () => {
  it("strips root prefix with slash separators", () => {
    expect(relativeUnderRoot("/proj/a/b.ts", "/proj", "linux")).toBe("a/b.ts");
    expect(relativeUnderRoot("/proj", "/proj", "linux")).toBe(".");
  });
});
