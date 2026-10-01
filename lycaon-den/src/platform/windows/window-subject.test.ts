import { describe, expect, it, vi } from "vitest";
import { windowSubject } from "./window-subject.ts";

describe("windowSubject", () => {
  it("accepts a complete durable session subject", () => {
    vi.stubGlobal("window", {
      location: {
        search: "?window_subject=session&project_id=project-1&session_id=session-1&view_id=4",
      },
    });
    expect(windowSubject()).toEqual({
      kind: "session",
      projectId: "project-1",
      sessionId: "session-1",
      viewId: "4",
    });
    vi.unstubAllGlobals();
  });

  it("requires root identity and a path for a file subject", () => {
    vi.stubGlobal("window", {
      location: {
        search: "?window_subject=file&project_id=project-1",
      },
    });
    expect(windowSubject()).toBeNull();
    vi.unstubAllGlobals();
  });

  it("parses the file identity and required view identity", () => {
    vi.stubGlobal("window", {
      location: {
        search: "?window_subject=file&project_id=project-1&root_id=root-1&path=src%2Fa.ts&view_id=7",
      },
    });
    expect(windowSubject()).toEqual({
      kind: "file",
      projectId: "project-1",
      rootId: "root-1",
      path: "src/a.ts",
      viewId: "7",
    });
    vi.unstubAllGlobals();
  });

  it("accepts a project-scoped Context subject without a session", () => {
    vi.stubGlobal("window", {
      location: {
        search: "?window_subject=context&project_id=project-1&stage_id=files&view_id=8",
      },
    });
    expect(windowSubject()).toEqual({
      kind: "context",
      projectId: "project-1",
      stageId: "files",
      viewId: "8",
    });
    vi.unstubAllGlobals();
  });
});
