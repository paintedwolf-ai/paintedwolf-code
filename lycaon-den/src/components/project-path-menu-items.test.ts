import { describe, expect, it, vi } from "vitest";
import { projectPathMenuItems } from "./project-path-menu-items.ts";
import { resetHostIdentityForTest } from "../platform/connection/host-identity.ts";
import { testHostInfo } from "../platform/connection/host-identity-test.ts";

const addToChat = vi.fn();

vi.mock("../platform/files/reveal-in-file-manager.ts", async (importOriginal) => {
  const actual =
    await importOriginal<
      typeof import("../platform/files/reveal-in-file-manager.ts")
    >();
  return {
    ...actual,
    revealInFileManager: vi.fn(async () => ({ status: "revealed" as const })),
  };
});

vi.mock("../platform/runtime.ts", () => ({
  tauriPlatform: () => "macos" as const,
  isTauriRuntime: () => true,
}));

vi.mock("../utils/clipboard.ts", () => ({
  copyTextToClipboard: vi.fn(async () => {}),
}));

vi.mock("../chat/composer/add-to-chat.ts", () => ({
  addToChat: (...args: unknown[]) => addToChat(...args),
}));

describe("projectPathMenuItems", () => {
  it("builds Reveal + both copy forms without Open by default", () => {
    const items = projectPathMenuItems({
      absolutePath: "/proj/a",
      projectId: "p1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });
    expect(items.map((i) => i.label)).toEqual([
      "Reveal in tree",
      "Open in",
      "Copy path",
      "Copy relative path",
      "Add to chat",
    ]);
    expect(items.map((i) => i.testId)).toEqual([
      "path-menu-reveal-tree",
      "open-in-menu",
      "path-menu-copy-path",
      "path-menu-copy-relative-path",
      "menu-add-to-chat",
    ]);
  });

  it("omits the file manager when the host does not share this device", () => {
    resetHostIdentityForTest(testHostInfo({ capabilities: [] }));
    const items = projectPathMenuItems({
      absolutePath: "/proj/a",
      projectId: "p1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });
    expect(items.map((i) => i.testId)).not.toContain("open-in-menu");
    expect(items.map((i) => i.testId)).toContain("path-menu-reveal-tree");
  });

  it("copies the absolute path and the root-relative path respectively", async () => {
    const { copyTextToClipboard } = await import("../utils/clipboard.ts");
    vi.mocked(copyTextToClipboard).mockClear();
    const items = projectPathMenuItems({
      absolutePath: "/proj/src/main.go",
      projectId: "p1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });
    items.find((i) => i.testId === "path-menu-copy-path")?.onSelect!();
    items.find((i) => i.testId === "path-menu-copy-relative-path")?.onSelect!();
    expect(vi.mocked(copyTextToClipboard).mock.calls.map((c) => c[0])).toEqual([
      "/proj/src/main.go",
      "src/main.go",
    ]);
  });

  it("picks the deepest root when roots nest", async () => {
    const { copyTextToClipboard } = await import("../utils/clipboard.ts");
    vi.mocked(copyTextToClipboard).mockClear();
    const items = projectPathMenuItems({
      absolutePath: "/proj/pkg/api/types.go",
      projectId: "p1",
      entryKind: "file",
      rootRefs: [
        { id: "root-a", path: "/proj" },
        { id: "root-b", path: "/proj/pkg" },
      ],
    });
    items.find((i) => i.testId === "path-menu-copy-relative-path")?.onSelect!();
    expect(vi.mocked(copyTextToClipboard)).toHaveBeenCalledWith(
      "api/types.go",
    );
  });

  it("omits the relative form for a root folder and out-of-jail paths", () => {
    const rootRow = projectPathMenuItems({
      absolutePath: "/proj",
      projectId: "p1",
      entryKind: "folder",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });
    expect(
      rootRow.some((i) => i.testId === "path-menu-copy-relative-path"),
    ).toBe(false);

    const outside = projectPathMenuItems({
      absolutePath: "/elsewhere/a.go",
      projectId: "p1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });
    expect(
      outside.some((i) => i.testId === "path-menu-copy-relative-path"),
    ).toBe(false);
    expect(outside.some((i) => i.testId === "path-menu-copy-path")).toBe(true);
  });

  it("prepends Open when onOpen is provided", () => {
    const onOpen = vi.fn();
    const items = projectPathMenuItems({
      absolutePath: "/proj/a.go",
      projectId: "p1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
      onOpen,
    });
    expect(items[0]?.testId).toBe("path-menu-open");
    items[0]?.onSelect!();
    expect(onOpen).toHaveBeenCalled();
  });

  it("adds Add to chat for in-jail path-file when rootRefs resolve", () => {
    addToChat.mockClear();
    const items = projectPathMenuItems({
      absolutePath: "/proj/src/main.go",
      projectId: "proj-1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });
    const add = items.find((i) => i.testId === "menu-add-to-chat");
    expect(add?.label).toBe("Add to chat");
    add?.onSelect!();
    expect(addToChat).toHaveBeenCalledWith({
      kind: "path-file",
      projectId: "proj-1",
      rootId: "root-a",
      path: "src/main.go",
      name: "main.go",
    });
  });

  it("adds path-folder ref when entryKind is folder", () => {
    addToChat.mockClear();
    const items = projectPathMenuItems({
      absolutePath: "/proj",
      projectId: "proj-1",
      entryKind: "folder",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });
    const add = items.find((i) => i.testId === "menu-add-to-chat");
    add?.onSelect!();
    expect(addToChat).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: "path-folder",
        rootId: "root-a",
        path: ".",
      }),
    );
  });

  it("uses an exact destination for chat-scoped path chrome", () => {
    addToChat.mockClear();
    const items = projectPathMenuItems({
      absolutePath: "/proj/src/main.go",
      projectId: "proj-1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
      chatDestination: { projectId: "proj-1", sessionId: "sess-1" },
    });

    items.find((item) => item.testId === "menu-add-to-chat")?.onSelect!();
    expect(addToChat).toHaveBeenCalledWith(
      expect.objectContaining({ kind: "path-file" }),
      { destination: { projectId: "proj-1", sessionId: "sess-1" } },
    );
  });

  it("omits Add to chat when chat-scoped chrome lacks an exact destination", () => {
    const items = projectPathMenuItems({
      absolutePath: "/proj/src/main.go",
      projectId: "proj-1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
      chatDestination: null,
    });

    expect(items.some((item) => item.testId === "menu-add-to-chat")).toBe(false);
  });

  it("omits Add to chat when the path is outside rootRefs", () => {
    const items = projectPathMenuItems({
      absolutePath: "/other/secret.go",
      projectId: "proj-1",
      entryKind: "file",
      rootRefs: [{ id: "root-a", path: "/proj" }],
    });
    expect(items.some((i) => i.testId === "menu-add-to-chat")).toBe(false);
  });
});


it("reveals an addressed project file without opening an editor", async () => {
  const { registerOpenSourceSink, registerOpenSourceProjectLookup } = await import("../platform/navigation/open-source.ts");
  const opened = vi.fn();
  const stop = registerOpenSourceSink(opened);
  const stopLookup = registerOpenSourceProjectLookup(() => ({ roots: [{ id: "r1", path: "/proj" }] }));
  try {
    const items = projectPathMenuItems({ absolutePath: "/proj/src/main.ts", projectId: "p1",
      entryKind: "file", rootRefs: [{ id: "r1", path: "/proj" }] });
    items.find((item) => item.testId === "path-menu-reveal-tree")?.onSelect?.();
    expect(opened).toHaveBeenCalledWith({ action: "reveal", projectId: "p1", rootId: "r1", path: "src/main.ts", absolutePath: "/proj/src/main.ts", entryKind: "file" });
  } finally { stop(); stopLookup(); }
});


it("omits tree reveal outside attached roots and in worker workspaces", () => {
  for (const overrides of [{ absolutePath: "/elsewhere/file" }, { jobId: "worker-1" }]) {
    const items = projectPathMenuItems({ absolutePath: "/proj/file", projectId: "p1",
      entryKind: "file", rootRefs: [{ id: "r1", path: "/proj" }], ...overrides });
    expect(items.some((item) => item.testId === "path-menu-reveal-tree")).toBe(false);
  }
});


it("keeps an explicit outer root binding when another attached root is nested inside it", async () => {
  const { registerOpenSourceSink, registerOpenSourceProjectLookup } = await import("../platform/navigation/open-source.ts");
  const roots = [{ id: "outer", path: "/repo" }, { id: "inner", path: "/repo/package" }];
  const sink = vi.fn();
  const stop = registerOpenSourceSink(sink);
  const stopLookup = registerOpenSourceProjectLookup(() => ({ roots }));
  try {
    const items = projectPathMenuItems({ absolutePath: "/repo/package/file.ts", projectId: "p1", rootId: "outer", entryKind: "file", rootRefs: roots });
    items.find((item) => item.testId === "path-menu-reveal-tree")?.onSelect?.();
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ rootId: "outer", path: "package/file.ts" }));
    items.find((item) => item.testId === "menu-add-to-chat")?.onSelect?.();
    expect(addToChat).toHaveBeenLastCalledWith(expect.objectContaining({ rootId: "outer", path: "package/file.ts" }));
  } finally { stop(); stopLookup(); }
});

it("uses the same worker capabilities for every path-link surface", async () => {
  const { projectPathChatRef } = await import("./project-path-menu-items.ts");
  const options = { absolutePath: "/repo/file.ts", projectId: "p1", rootId: "r", jobId: "worker", entryKind: "file" as const, rootRefs: [{ id: "r", path: "/repo" }] };
  const items = projectPathMenuItems(options);
  expect(items.some((item) => item.testId === "path-menu-reveal-tree")).toBe(false);
  expect(items.some((item) => item.testId === "menu-add-to-chat")).toBe(false);
  expect(items.find((item) => item.testId === "open-in-menu")?.submenu?.every((item) => item.disabled)).toBe(true);
  expect(items.find((item) => item.testId === "path-menu-copy-path")?.disabled).toBe(true);
  expect(projectPathChatRef(options)).toBeNull();
});
