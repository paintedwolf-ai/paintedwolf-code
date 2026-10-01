import { beginSourceNavigation } from "./source-navigation-intent.ts";
import type { SourceDirListing } from "../../api/types.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  classifyPathKind,
  openSourceLocation,
  registerOpenSourceProjectLookup,
  registerOpenSourceSink,
  resetOpenSourceForTests,
} from "./open-source.ts";
import {
  resetExternalOpenPrefsForTests,
} from "../../settings/editor/external-open-prefs.ts";

const roots = {
  roots: [{ id: "root-1", path: "/Users/me/repo", is_primary: true }],
};

describe("openSourceLocation", () => {
  beforeEach(() => {
    resetOpenSourceForTests();
    resetExternalOpenPrefsForTests();
  });

  it("no-ops when the target names no path", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);

    const got = await openSourceLocation({
      intent: "transient",
      projectId: "p1",
    });

    expect(got).toEqual({ status: "noop", reason: "no_path" });
    expect(sink).not.toHaveBeenCalled();
  });

  // The job id locates edits in the worker overlay.
  it("carries the worker job id onto the in-app request", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);

    await openSourceLocation({
      intent: "transient",
      projectId: "p1",
      path: "internal/cli/root.go",
      jobId: "job-9",
    });

    expect(sink).toHaveBeenCalledWith(
      expect.objectContaining({ path: "internal/cli/root.go", jobId: "job-9" }),
    );
  });

  // The source read carries deletion history; navigation only addresses the path.
  it("opens an absent path in the app", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);
    const result = await openSourceLocation({ projectId: "p1", path: "gone.ts", line: 12, intent: "permanent" });
    expect(result.status).toBe("opened-in-app");
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ path: "gone.ts", line: 12 }));
    expect(sink.mock.calls[0]![0]).not.toHaveProperty("fileId");
    expect(sink.mock.calls[0]![0]).not.toHaveProperty("versionId");
  });

  it("omits jobId when the edit was not made on a worker branch", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);

    await openSourceLocation({
      intent: "transient", projectId: "p1", path: "src/a.ts" });

    expect(sink.mock.calls[0]![0]).not.toHaveProperty("jobId");
  });

  it("dispatches in-app sink by default after resolve", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);

    const got = await openSourceLocation({
      intent: "transient",
      projectId: "p1",
      path: "src/a.ts",
      line: 12,
    });

    expect(got).toEqual({ status: "opened-in-app" });
    expect(sink).toHaveBeenCalledWith({
      intent: "transient",
      projectId: "p1",
      path: "src/a.ts",
      absolutePath: "/Users/me/repo/src/a.ts",
      rootId: "root-1",
      line: 12,
    });
  });

  it("preserves a durable root binding for duplicate multi-root paths", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => ({
      roots: [
        { id: "root-1", path: "/Users/me/repo", is_primary: true },
        { id: "root-2", path: "/Users/me/other", is_primary: false },
      ],
    }));

    await openSourceLocation({
      intent: "transient",
      projectId: "p1",
      rootId: "root-2",
      path: "src/a.ts",
    });

    expect(sink).toHaveBeenCalledWith(expect.objectContaining({
      absolutePath: "/Users/me/other/src/a.ts",
      rootId: "root-2",
    }));
  });

  it("keeps file navigation in the app independently of external editor settings", async () => {
    resetExternalOpenPrefsForTests({ externalEditor: "vscode" });
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);
    const result = await openSourceLocation({ intent: "transient", projectId: "p1", path: "src/a.ts", line: 3 });
    expect(result).toEqual({ status: "opened-in-app" });
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ path: "src/a.ts", line: 3 }));
  });

  it("fails closed when resolve misses roots", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);

    const got = await openSourceLocation(
      { intent: "transient", projectId: "p1", path: "/tmp/outside.ts" },
    );

    expect(got).toEqual({ status: "noop", reason: "resolve_failed" });
    expect(sink).not.toHaveBeenCalled();
  });

  it("fails closed when project lookup misses", async () => {
    registerOpenSourceProjectLookup(() => undefined);
    const got = await openSourceLocation({
      intent: "transient",
      projectId: "missing",
      path: "a.ts",
    });
    expect(got).toEqual({ status: "noop", reason: "no_project" });
  });
});


describe("source navigation actions", () => {
  beforeEach(() => {
    resetOpenSourceForTests();
    resetExternalOpenPrefsForTests();
    registerOpenSourceProjectLookup(() => ({ roots: [
      { id: "primary", path: "/repo", is_primary: true },
      { id: "other", path: "/other", label: "other" },
    ] }));
  });

  it.each(["@other/src/file.ts", "/other/src/file.ts"])("normalizes %s to one root-relative destination", async (path) => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    await openSourceLocation({ projectId: "p", path, intent: "permanent" });
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ rootId: "other", path: "src/file.ts" }));
  });

  it.each([".", "./", "src/..", "/repo", "@other", "@other/."])("reveals root %s without opening an editor buffer", async (path) => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    await openSourceLocation({ projectId: "p", path, intent: "permanent" });
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ action: "reveal", entryKind: "folder", path: ".", rootId: path.startsWith("@") ? "other" : "primary" }));
  });

  it.each([true, false])("uses host directory metadata for an untyped tool path (directory=%s)", async (isDir) => {
    const sink = vi.fn();
    const browseProjectSource = vi.fn().mockResolvedValue({ entries: [{ name: "target", is_dir: isDir }] });
    registerOpenSourceSink(sink);
    await openSourceLocation({ projectId: "p", path: "@other/src/target", entryKind: "unknown", intent: "permanent" }, { client: { browseProjectSource } });
    expect(browseProjectSource).toHaveBeenCalledWith("p", { rootId: "other", dir: "src" });
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ rootId: "other", path: "src/target" }));
    expect(sink.mock.calls[0]![0].action).toBe(isDir ? "reveal" : undefined);
  });

  it("does not let a slow directory lookup override a newer click", async () => {
    let finish!: (value: SourceDirListing) => void;
    const browseProjectSource = vi.fn(() => new Promise<SourceDirListing>((resolve) => { finish = resolve; }));
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    const pending = openSourceLocation({ projectId: "p", path: "old", entryKind: "unknown", intent: "permanent" }, { client: { browseProjectSource } });
    await openSourceLocation({ projectId: "p", path: "new.ts", intent: "permanent" });
    finish({ entries: [{ name: "old", is_dir: true }] } as SourceDirListing);
    expect(await pending).toEqual({ status: "superseded" });
    expect(sink).toHaveBeenCalledTimes(1);
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ path: "new.ts" }));
  });

  it("reports a failed directory lookup without creating a broken buffer", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    const result = await openSourceLocation({ projectId: "p", path: "src", entryKind: "unknown", intent: "permanent" }, {
      client: { browseProjectSource: vi.fn().mockRejectedValue(new Error("Source unavailable")) },
    });
    expect(result).toEqual({ status: "rejected", reason: "Source unavailable" });
    expect(sink).not.toHaveBeenCalled();
  });

  it.each(["open", "reveal"] as const)("routes folder %s through the same in-app sink", async (action) => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    await openSourceLocation({ projectId: "p", path: "src", entryKind: "folder", intent: "permanent", action });
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ action: "reveal", entryKind: "folder", path: "src" }));
  });

  it("retains only the latest request before the workspace mounts", async () => {
    await openSourceLocation({ projectId: "p", path: "src", entryKind: "folder", intent: "permanent" });
    await openSourceLocation({ projectId: "p", path: "file.ts", intent: "permanent" });
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    expect(sink).toHaveBeenCalledTimes(1);
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ path: "file.ts" }));
  });

  it("drops a queued destination after a newer navigation elsewhere", async () => {
    await openSourceLocation({ projectId: "p", path: "older.ts", intent: "permanent" });
    beginSourceNavigation();
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    expect(sink).not.toHaveBeenCalled();
  });

  it("refuses worker tree reveal without routing to the primary tree", async () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    const result = await openSourceLocation({ projectId: "p", path: "file.ts", jobId: "worker", action: "reveal", intent: "permanent" });
    expect(result.status).toBe("rejected");
    expect(sink).not.toHaveBeenCalled();
  });

  it("classifies file extensions and folders without host round-trips", () => {
    expect(classifyPathKind(".")).toBe("folder");
    expect(classifyPathKind("src/")).toBe("folder");
    expect(classifyPathKind("docs/accessibility.md")).toBe("file");
    expect(classifyPathKind(".gitignore")).toBe("file");
    expect(classifyPathKind("src/unknown")).toBe("unknown");
  });

  it("skips host directory lookup for paths with extensions or explicit reveal", async () => {
    const sink = vi.fn();
    const browseProjectSource = vi.fn();
    registerOpenSourceSink(sink);
    await openSourceLocation({ projectId: "p", path: "docs/accessibility.md", entryKind: "unknown", intent: "permanent" }, { client: { browseProjectSource } });
    expect(browseProjectSource).not.toHaveBeenCalled();
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ path: "docs/accessibility.md" }));

    await openSourceLocation({ projectId: "p", path: "src/folder", entryKind: "unknown", action: "reveal", intent: "permanent" }, { client: { browseProjectSource } });
    expect(browseProjectSource).not.toHaveBeenCalled();
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ action: "reveal", path: "src/folder" }));
  });
});
