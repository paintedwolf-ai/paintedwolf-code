import "../../test/document-outbox-fixture.ts";
import { resetProjectFilesViewTest, PROJECT, ROOTS, PROJECT_SOURCE_IDENTITY, deferred } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, it, vi, expect } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { render, waitFor } from "@solidjs/testing-library";
import { ProjectFilesView } from "./ProjectFilesView.tsx";

import { openSourceInFilesStage } from "./project-files-open.ts";

import { projectFilesState } from "../documents/files-buffer-state.ts";

import { LycaonApiError } from "../../api/http.ts";
import { filesBufferBase } from "../documents/project-files-buffers.ts";

describe("ProjectFilesView single buffer loader", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  it("reads a source and claims its document exactly once per open", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const base = getLycaonClient()!;
    const getProjectSource = vi.fn(async (_p: string, path: string) => ({
      ...PROJECT_SOURCE_IDENTITY,
      root_id: "r1",
      path,
      file_id: `file-${path}`,
      content: "hello",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 5,
      sha256: "sha-hello",
      encoding: "utf-8" as const,
    }));
    const openEditorDocument = vi.fn(base.openEditorDocument);
    const client = stubFilesClient({
      ...base,
      getProjectSource,
      openEditorDocument,
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));
    await Promise.resolve();

    openSourceInFilesStage({
      request: {
        projectId: PROJECT,
        rootId: "r1",
        path: "a.txt",
        absolutePath: "/repo/a.txt",
        intent: "permanent",
        line: 3,
      },
      rootLabelFor: () => "repo",
      navigate: () => { },
    });

    await waitFor(() => {
      const key = projectFilesState(PROJECT).activeKey;
      expect(key && projectFilesState(PROJECT).byKey[key]?.loading).toBe(false);
    });
    const reads = getProjectSource.mock.calls.filter(
      (call) => call[1] === "a.txt",
    );
    expect(reads).toHaveLength(1);
    const claims = openEditorDocument.mock.calls.filter(
      (call) => (call[1] as { path: string }).path === "a.txt",
    );
    expect(claims).toHaveLength(1);
  });
});

describe("ProjectFilesView buffer loader workspace transitions", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  function sourceFor(path: string, workspaceId = "workspace-1") {
    return {
      workspace_id: workspaceId,
      workspace_kind: "project" as const,
      root_id: "r1",
      path,
      file_id: `file-${path}`,
      content: "hello",
      over_limit: false,
      writable: true,
      binary: false,
      size_bytes: 5,
      sha256: "sha-hello",
      encoding: "utf-8" as const,
    };
  }

  function mountWith(overrides: Record<string, unknown>) {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const client = stubFilesClient({ ...getLycaonClient()!, ...overrides });
    const view = render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));
    return { client, view };
  }

  function openPath(path: string) {
    openSourceInFilesStage({
      request: {
        projectId: PROJECT,
        rootId: "r1",
        path,
        absolutePath: `/repo/${path}`,
        intent: "permanent",
      },
      rootLabelFor: () => "repo",
      navigate: () => { },
    });
  }

  it("browses a directory address instead of failing it as a file", async () => {
    // Listings answer for the directory asked, root and nested alike.
    const browseProjectSource = vi.fn(
      async (_projectId: string, input: { rootId: string; dir: string }) => ({
        workspace_id: "workspace-1",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries: [],
      }),
    );
    const getProjectSource = vi.fn(async () => {
      throw new LycaonApiError("no file", 404, "source_not_found");
    });
    mountWith({ getProjectSource, browseProjectSource });
    await waitFor(() => expect(browseProjectSource).toHaveBeenCalled());

    openPath("src/nested");

    // Root browsing adds another call.
    await waitFor(() =>
      expect(
        browseProjectSource.mock.calls.some(
          (call) =>
            (call as unknown as [string, { dir?: string }])[1]?.dir ===
            "src/nested",
        ),
      ).toBe(true)
    );
    await waitFor(() => expect(projectFilesState(PROJECT).order).toHaveLength(0));
  });

  it("keeps the painted source when the document claim fails", async () => {
    const getProjectSource = vi.fn(async (_p: string, path: string) => sourceFor(path));
    const syncEditorDocument = vi.fn(async () => {
      throw new Error("lease refused");
    });
    mountWith({ getProjectSource, syncEditorDocument });
    await Promise.resolve();

    openPath("a.txt");

    await waitFor(() => {
      const key = projectFilesState(PROJECT).activeKey;
      const buf = key ? projectFilesState(PROJECT).byKey[key] : undefined;
      expect(filesBufferBase(buf!)).toBe("hello");
      expect(buf?.loadError).toBe(null);
      expect(buf?.documentId).toBe(null);
    });
  });

  it("does not let a superseded read overwrite the buffer that replaced it", async () => {
    const gate = deferred<void>();
    const getProjectSource = vi.fn(async (_p: string, path: string) => {
      if (path === "slow.txt") {
        await gate.promise;
        return sourceFor(path);
      }
      return { ...sourceFor(path), content: "fast" };
    });
    mountWith({ getProjectSource });
    await Promise.resolve();

    openPath("slow.txt");
    openPath("fast.txt");
    await waitFor(() => {
      const key = projectFilesState(PROJECT).activeKey;
      expect(key && filesBufferBase(projectFilesState(PROJECT).byKey[key]!)).toBe("fast");
    });

    gate.resolve();
    await Promise.resolve();
    const activeKey = projectFilesState(PROJECT).activeKey;
    expect(activeKey && projectFilesState(PROJECT).byKey[activeKey]?.path).toBe("fast.txt");
  });
});
