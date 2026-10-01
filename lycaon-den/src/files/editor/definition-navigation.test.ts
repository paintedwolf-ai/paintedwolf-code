// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { createRoot, onCleanup } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { SourceDefinitionCandidate } from "../../api/types.ts";
import type { FileBuffer, ProjectFilesState } from "../documents/files-buffer-state.ts";
import { createDefinitionNavigation } from "./definition-navigation.ts";

const { openBuffer, pushJump, getView, markPending, clearPending } = vi.hoisted(() => ({
  openBuffer: vi.fn(() => "destination"), pushJump: vi.fn(), getView: vi.fn(),
  markPending: vi.fn(), clearPending: vi.fn(),
}));
vi.mock("../documents/project-files-buffers.ts", () => ({ openFilesBuffer: openBuffer }));
vi.mock("../components/project-files-jump-bridge.ts", () => ({ pushProjectJump: pushJump }));
vi.mock("./files-editor-host.ts", () => ({ getFilesEditorView: getView }));
vi.mock("../../components/source/editor/codemirror-theme.ts", () => ({
  markSymbolPending: markPending, clearSymbolPending: clearPending,
}));
vi.mock("@codemirror/autocomplete", () => ({ closeCompletion: vi.fn() }));

type DefinitionResponse = { candidates: SourceDefinitionCandidate[]; truncated: boolean };
function deferred() {
  let resolve!: (result: DefinitionResponse) => void;
  const promise = new Promise<DefinitionResponse>((yes) => { resolve = yes; });
  return { promise, resolve };
}
const candidate = (path: string): SourceDefinitionCandidate => ({
  root_id: "root", path, line: 5, kind: "function", snippet: "function resolve() {}",
});
function navigation(request: LycaonClient["resolveProjectSourceDefinition"]) {
  return createRoot(dispose => {
    const subject = createDefinitionNavigation({
      projectId: () => "project", state: () => ({ activeKey: "source" }) as ProjectFilesState,
      activeBuffer: () => ({ key: "source", kind: "text", rootId: "root", path: "source.ts" }) as FileBuffer,
      client: () => ({ resolveProjectSourceDefinition: request }) as LycaonClient,
      sourceSessionId: () => "session", rootLabelFor: () => "Project root",
      cursor: () => ({ line: 2, col: 3 }),
    });
    onCleanup(subject.clearNoticeTimer);
    subject.observeKeyboardAndBuffer();
    return { ...subject, dispose };
  });
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  getView.mockReturnValue({ state: EditorState.create({ doc: "resolve()", selection: { anchor: 3 } }) } as EditorView);
});
afterEach(() => vi.useRealTimers());

describe("definition navigation request lifetime", () => {
  it("ignores an older result after a newer lookup navigates", async () => {
    const earlier = deferred();
    const later = deferred();
    const request = vi.fn().mockReturnValueOnce(earlier.promise).mockReturnValueOnce(later.promise);
    const subject = navigation(request);
    try {
      const first = subject.runGoToDefinition();
      const second = subject.runGoToDefinition();
      later.resolve({ candidates: [candidate("new.ts")], truncated: false });
      await second;
      earlier.resolve({ candidates: [candidate("old.ts")], truncated: false });
      await first;
      expect(openBuffer).toHaveBeenCalledTimes(1);
      expect(openBuffer).toHaveBeenCalledWith("project", expect.objectContaining({ path: "new.ts" }));
      expect(pushJump).toHaveBeenCalledTimes(2);
      expect(subject.definitionNotice()).toBeNull();
    } finally { subject.dispose(); }
  });

  it("Escape clears the pending mark and prevents a late result from navigating", async () => {
    const pending = deferred();
    const subject = navigation(vi.fn(() => pending.promise));
    try {
      const request = subject.runGoToDefinition();
      const escape = new KeyboardEvent("keydown", { key: "Escape", cancelable: true });
      window.dispatchEvent(escape);
      expect(escape.defaultPrevented).toBe(true);
      expect(clearPending).toHaveBeenCalledTimes(1);
      pending.resolve({ candidates: [candidate("late.ts")], truncated: false });
      await request;
      expect(openBuffer).not.toHaveBeenCalled();
      expect(subject.definitionPicker()).toBeNull();
      expect(subject.definitionNotice()).toBeNull();
    } finally { subject.dispose(); }
    const afterDispose = new KeyboardEvent("keydown", { key: "Escape", cancelable: true });
    window.dispatchEvent(afterDispose);
    expect(afterDispose.defaultPrevented).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });
});
