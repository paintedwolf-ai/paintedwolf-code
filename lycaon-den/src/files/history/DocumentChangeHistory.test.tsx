import { assertNamedControl } from "../../test/controls.ts";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { EditorDocumentChanges } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { PRESENTATION_LOADING_GRACE_MS } from "../../ui/presentation.ts";
import { resetSurfaceQueriesForTests } from "../../ui/surface-query.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { DocumentChangeHistory } from "./DocumentChangeHistory.tsx";

vi.mock("../documents/editor-document.ts", () => ({ editorReplica: () => undefined }));

function page(title = "First edit", next?: string): EditorDocumentChanges {
  return { changes: [{ actor_kind: "agent", session_id: "session", session_title: title, turn: 1,
    revision: 2, epoch: 1, operation_id: title, person_id: "", client_id: "", reverted_by: "",
    created_at: "2026-09-13T10:00:00Z" }], next_cursor: next };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (cause: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

afterEach(() => { cleanup(); resetSurfaceQueriesForTests(); registerNoticePublisher(null); vi.useRealTimers(); });

describe("recent edits presentation", () => {
  it("does not insert loading text or empty padding for a fast empty response", async () => {
    vi.useFakeTimers();
    const read = deferred<EditorDocumentChanges>();
    const client = stubClient({ listEditorDocumentChanges: () => read.promise });
    render(() => <DocumentChangeHistory client={client} projectId="project" documentId="document" onApplied={() => {}} rows={[]} renderRow={() => null} />);
    expect(screen.queryByTestId("document-change-author")).toBeNull();
    await vi.advanceTimersByTimeAsync(PRESENTATION_LOADING_GRACE_MS - 1);
    expect(screen.queryByRole("status")).toBeNull();
    read.resolve({ changes: [], next_cursor: undefined });
    await vi.advanceTimersByTimeAsync(PRESENTATION_LOADING_GRACE_MS);
    expect(screen.queryByTestId("document-change-author")).toBeNull();
  });

  it("shows waiting feedback only after the shared grace period, then publishes edits", async () => {
    vi.useFakeTimers();
    const read = deferred<EditorDocumentChanges>();
    const client = stubClient({ listEditorDocumentChanges: () => read.promise });
    render(() => <DocumentChangeHistory client={client} projectId="project" documentId="document" onApplied={() => {}} rows={[]} renderRow={() => null} />);
    await vi.advanceTimersByTimeAsync(PRESENTATION_LOADING_GRACE_MS);
    read.resolve(page());
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByTestId("document-change-author").textContent).toBe("From First edit · turn 1");
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("retains edits when reopened and reports a failed refresh as a notice", async () => {
    vi.useFakeTimers();
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    const refresh = deferred<EditorDocumentChanges>();
    const list = vi.fn().mockResolvedValueOnce(page()).mockReturnValueOnce(refresh.promise);
    const client = stubClient({ listEditorDocumentChanges: list });
    const mount = () => render(() => <DocumentChangeHistory client={client} projectId="project" documentId="document" onApplied={() => {}} rows={[]} renderRow={() => null} />);
    const first = mount();
    await vi.advanceTimersByTimeAsync(0);
    first.unmount();
    mount();
    expect(screen.getByTestId("document-change-author").textContent).toBe("From First edit · turn 1");
    await vi.advanceTimersByTimeAsync(PRESENTATION_LOADING_GRACE_MS);
    expect(screen.queryByRole("status")).toBeNull();
    refresh.reject(new Error("History unavailable"));
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByTestId("document-change-author").textContent).toBe("From First edit · turn 1");
    const rows = selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "project")?.notices ?? [];
    expect(rows).toMatchObject([{ code: "document_change_history_unavailable", message: "History unavailable" }]);
  });

  it("does not publish an earlier document's late page after navigation", async () => {
    vi.useFakeTimers();
    const earlier = deferred<EditorDocumentChanges>();
    const list = vi.fn().mockResolvedValueOnce(page("First edit", "cursor-2")).mockReturnValueOnce(earlier.promise).mockResolvedValueOnce(page("Other document"));
    const client = stubClient({ listEditorDocumentChanges: list });
    const [documentId, setDocumentId] = createSignal("first");
    render(() => <DocumentChangeHistory client={client} projectId="project" documentId={documentId()} onApplied={() => {}} rows={[]} renderRow={() => null} />);
    await vi.advanceTimersByTimeAsync(0);
    fireEvent.click(assertNamedControl(screen.getByTestId("earlier-edits"), "button"));
    setDocumentId("other");
    expect(screen.queryByTestId("document-change-author")?.textContent).not.toBe("From First edit · turn 1");
    await vi.advanceTimersByTimeAsync(0);
    earlier.resolve(page("Old page"));
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByTestId("document-change-author").textContent).toBe("From Other document · turn 1");
    expect(screen.queryByTestId("document-change-author")?.textContent).not.toBe("From Old page · turn 1");
  });
});
