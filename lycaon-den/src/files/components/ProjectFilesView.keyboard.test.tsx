import "../../test/document-outbox-fixture.ts";
import { PROJECT, ROOTS, resetProjectFilesViewTest, PROJECT_SOURCE_IDENTITY } from "./project-files-view-test-harness.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { applyFilesBufferLoad, openFilesBuffer } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { resetFileVersionSelectionForTests, selectedFileVersion, setSelectedFileVersion, shownVersionComparison } from "../history/files-version-selection.ts";
import type { FileVersionView } from "../history/file-version.ts";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import { isComparisonOff } from "../review/review-pane.ts";
import { createSignal } from "solid-js";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";

const WALKED: FileVersionView = {
  versionId: "version-old", fileId: "file-main", rootId: "r1", path: "src/main.ts", op: "write",
  ts: "2026-08-26T10:00:00Z", beforeAvailability: "available", sha256: "old-sha", sizeBytes: 4,
  availability: "available", source: { kind: "text", before: "older\n", after: "old\n" }, initialComparison: "before",
};

function openLoaded(path: string, content: string, binary = false) {
  const key = openFilesBuffer(PROJECT, { intent: "permanent", rootId: "r1", rootLabel: "repo", fileId: "file-main", path });
  applyFilesBufferLoad(PROJECT, key, {
    ...PROJECT_SOURCE_IDENTITY, file_id: "file-main", version_id: "version-current", path, content,
    over_limit: false, writable: true, binary, size_bytes: content.length, sha256: "current-sha", ...(binary ? {} : { encoding: "utf-8" as const }),
  });
  return key;
}

function renderStage() {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={getLycaonClient()} />);
}

describe("Files stage keyboard", () => {
  beforeEach(() => {
    resetProjectFilesViewTest();
    resetFileVersionSelectionForTests();
  });

  it("flips the comparison a walk-opened version shows on the first press", async () => {
    const key = openLoaded("src/main.ts", "current\n");
    renderStage();
    await screen.findByTestId("files-editor");
    setSelectedFileVersion(PROJECT, key, WALKED);
    const trigger = () => screen.getByTestId("file-version-compare-trigger");
    await waitFor(() => expect(trigger().textContent).toContain("Before"));

    expect(invokeCommand("files.toggleComparison")).toBe("ran");
    expect(shownVersionComparison(PROJECT, key, WALKED)).toBe("current");
    await waitFor(() => expect(trigger().textContent).toContain("Current"));
    expect(isComparisonOff(PROJECT)).toBe(false);
  });

  it("turns the review comparison off when no past version is shown", async () => {
    openLoaded("src/main.ts", "current\n");
    renderStage();
    await screen.findByTestId("files-editor");
    expect(isComparisonOff(PROJECT)).toBe(false);
    expect(invokeCommand("files.toggleComparison")).toBe("ran");
    expect(isComparisonOff(PROJECT)).toBe(true);
  });

  it("leaves bare keys and Escape to the surface that has focus", async () => {
    const restore = vi.fn(async () => { throw new Error("restore must not run"); });
    Object.assign(getLycaonClient()!, { restoreProjectSourceVersion: restore });
    const key = openLoaded("src/main.ts", "current\n");
    renderStage();
    await screen.findByTestId("files-editor");
    setSelectedFileVersion(PROJECT, key, WALKED);
    await screen.findByTestId("file-version-compare-trigger");

    for (const pressed of ["[", "]", "ArrowLeft", "ArrowRight", "c", "r"]) {
      const event = fireEvent.keyDown(document.body, { key: pressed });
      expect(event).toBe(true);
    }
    const composer = document.createElement("textarea");
    document.body.append(composer);
    expect(fireEvent.keyDown(composer, { key: "Escape" })).toBe(true);

    expect(selectedFileVersion(PROJECT, key)?.versionId).toBe("version-old");
    expect(shownVersionComparison(PROJECT, key, WALKED)).toBe("before");
    expect(restore).not.toHaveBeenCalled();
  });

  it("answers files commands only while its surface is active", async () => {
    openLoaded("src/main.ts", "current\n");
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const [presence, setPresence] = createSignal<ResidentPresence>("active");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={getLycaonClient()} />
      </ResidentPresenceProvider>
    ));
    await screen.findByTestId("files-editor");

    setPresence("pending");
    expect(invokeCommand("files.toggleComparison")).toBe("missing");
    expect(isComparisonOff(PROJECT)).toBe(false);

    setPresence("active");
    expect(invokeCommand("files.toggleComparison")).toBe("ran");
    expect(isComparisonOff(PROJECT)).toBe(true);
  });

  it("changes line endings only on an editable text tab", async () => {
    const key = openLoaded("assets/blob.bin", "", true);
    renderStage();
    await waitFor(() => expect(projectFilesState(PROJECT).byKey[key]?.kind).toBe("info"));
    const before = projectFilesState(PROJECT).byKey[key]!.eol;

    invokeCommand("files.toggleLineEndings");
    const buffer = projectFilesState(PROJECT).byKey[key]!;
    expect(buffer.eol).toBe(before);
    expect(buffer.dirty).toBe(false);
  });
});
