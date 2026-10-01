import "../../test/document-outbox-fixture.ts";
import {
  PROJECT,
  ROOTS,
  loadedBuffer,
  resetProjectFilesViewTest,
} from "./project-files-view-test-harness.ts";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@solidjs/testing-library";
import type { AgentSessionPresence } from "../../api/types.ts";
import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { createAppStore } from "../../store/app-state.ts";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { applyAgentPresenceEvent, resetAgentPresenceForTests } from "./agent-presence-store.ts";

let revision = 0;

function publish(presence: Partial<AgentSessionPresence> & { session_id: string }): void {
  applyAgentPresenceEvent({
    project_id: PROJECT,
    session_id: presence.session_id,
    revision: ++revision,
    presence: { turn: 1, activities: [], reads: [], intents: [], worker_drafts: [], ...presence },
  });
}

function renderView() {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  const view = render(() => (
    <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={null} />
  ));
  return { view };
}

describe("ProjectFilesView agent presence", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(1_000);
    resetProjectFilesViewTest();
    resetAgentPresenceForTests();
    resetEditorPrefsForTests();
  });

  afterEach(() => {
    cleanup();
    resetAgentPresenceForTests();
    resetEditorPrefsForTests();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("retains agent facts in the accessible name without flags or filename highlights", async () => {
    loadedBuffer({ path: "src/main.ts" });
    const { view } = renderView();
    publish({
      session_id: "s1",
      title: "Fix login",
      activities: [{ tool_call_id: "c1", tool: "edit", root_id: "r1", path: "src/main.ts", kind: "editing" }],
      intents: [{
        id: "i1", tool_call_id: "c1", tool: "edit", operation: "edit", state: "awaiting_approval",
        root_id: "r1", path: "src/main.ts", extent: "range", ranges: [{ start_line: 2, end_line: 3 }],
      }],
      reads: [{
        id: "r1", sequence: 1, tool_call_id: "c0", tool: "read", root_id: "r1", path: "src/main.ts",
        extent: "whole_file", ranges: [], stale: false,
      }],
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByTestId("files-tab-presence")).toBeNull();
    expect(document.querySelector(".den-files-tab__header")?.textContent).toBe("");
    expect(screen.getByTestId("files-tab").getAttribute("aria-label")).toContain("waiting for your approval");
    expect(document.querySelector(".den-files-tab__name.den-files-agent-name")).toBeNull();
    expect(screen.getByTestId("files-tab").getAttribute("aria-label")).toContain("read this turn");
    view.unmount();
  });

  it("follows the file names setting", async () => {
    resetEditorPrefsForTests({ agentActivityKinds: { fileNames: false } });
    loadedBuffer({ path: "src/main.ts" });
    const { view } = renderView();
    publish({
      session_id: "s1",
      activities: [{ tool_call_id: "c1", tool: "read", root_id: "r1", path: "src/main.ts", kind: "reading" }],
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByTestId("files-tab-presence")).toBeNull();
    expect(document.querySelector(".den-files-agent-name")).toBeNull();
    view.unmount();
  });
});
