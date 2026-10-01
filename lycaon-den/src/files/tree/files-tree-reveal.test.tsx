import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SourceDirListing } from "../../api/types.ts";
import { createFilesTreeReveal } from "./files-tree-reveal.ts";
import { connectSourceTreeWorkspace, resetSourceTreeStoreForTests } from "./source-tree-store.ts";

type TreeBuffer = ReturnType<Parameters<typeof createFilesTreeReveal>[0]["buffers"]>[number];
const file = (overrides: Partial<TreeBuffer> = {}): TreeBuffer => ({
  kind: "text", rootId: "root", path: "src/file.ts", sourcePresent: false, ...overrides,
});
const listing = (entries: SourceDirListing["entries"]): SourceDirListing => ({
  workspace_id: "workspace", root_id: "root", dir: "src", watch_complete: true, entries,
});

afterEach(() => { cleanup(); resetSourceTreeStoreForTests(); });

function setup(initial: TreeBuffer[], result = listing([{ name: "file.ts", is_dir: false }])) {
  const [buffers, setBuffers] = createSignal(initial);
  const [available, setAvailable] = createSignal(true);
  const [roots, setRoots] = createSignal(["root"]);
  const [deleted, setDeleted] = createSignal<string[]>([]);
  const browse = vi.fn(async () => result);
  let reveal!: ReturnType<typeof createFilesTreeReveal>;
  render(() => {
    reveal = createFilesTreeReveal({
      projectId: "project", workspaceId: () => "workspace", available,
      rootIds: roots, buffers, deletedPaths: deleted, browse,
    });
    return null;
  });
  return { reveal, browse, setBuffers, setAvailable, setRoots, setDeleted };
}

describe("tab tree availability", () => {
  it.each(["walk", "trust", "chat"] as const)("disables %s tabs without browsing synthetic paths", (kind) => {
    const buffer = file({ kind });
    const { reveal, browse } = setup([buffer]);
    expect(reveal.action(() => buffer, vi.fn())).toMatchObject({ disabled: true });
    expect(browse).not.toHaveBeenCalled();
  });

  it("disables worker tabs, unavailable roots, and windows without a tree", () => {
    const buffer = file({ jobId: "worker" });
    const { reveal, browse, setAvailable, setRoots } = setup([buffer]);
    expect(reveal.unavailable(buffer)).toBeTruthy();
    setRoots([]);
    expect(reveal.unavailable(file())).toBeTruthy();
    setAvailable(false);
    expect(reveal.unavailable(file({ sourcePresent: true }))).toBeTruthy();
    expect(browse).not.toHaveBeenCalled();
  });

  it.each(["text", "image", "info", "diff"] as const)("reveals a %s view through a collapsed parent when its file exists", async (kind) => {
    const buffer = file({ kind });
    const { reveal, browse } = setup([buffer]);
    await waitFor(() => expect(reveal.unavailable(buffer)).toBeNull());
    expect(browse).toHaveBeenCalledExactlyOnceWith("root", "src");
    const select = vi.fn();
    const action = reveal.action(() => buffer, select);
    if ("onSelect" in action) action.onSelect();
    expect(select).toHaveBeenCalledExactlyOnceWith("root", "src/file.ts");
  });

  it("disables missing historical files but enables review tombstones", async () => {
    const buffer = file({ kind: "diff" });
    const { reveal, browse, setDeleted } = setup([buffer], listing([]));
    await waitFor(() => expect(browse).toHaveResolved());
    expect(reveal.unavailable(buffer)).toBe("This file is not in the current tree.");
    setDeleted([buffer.path]);
    expect(reveal.unavailable(buffer)).toBeNull();
    setDeleted([]);
    expect(reveal.unavailable(buffer)).toBeTruthy();
  });

  it("shares tree listings and rechecks an enabled action after the file is deleted", async () => {
    const buffer = file({ sourcePresent: true });
    const treeBrowse = vi.fn(async () => listing([{ name: "file.ts", is_dir: false }]));
    const tree = connectSourceTreeWorkspace({ projectId: "project", workspaceId: "workspace", browse: treeBrowse });
    await tree.load("root", "src");
    const { reveal, browse } = setup([buffer]);
    const select = vi.fn();
    const action = reveal.action(() => buffer, select);
    expect(action).toHaveProperty("onSelect");
    tree.confirm({ root_id: "root", path: buffer.path, op: "delete", is_dir: false, origin: "agent", changed_at: "2026-09-15T00:00:00Z" });
    expect(reveal.unavailable(buffer)).toBeTruthy();
    if ("onSelect" in action) action.onSelect();
    expect(select).not.toHaveBeenCalled();
    expect(browse).not.toHaveBeenCalled();
    tree.disconnect();
  });
});
