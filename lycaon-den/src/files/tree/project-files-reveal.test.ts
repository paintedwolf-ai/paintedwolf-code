import { beginSourceNavigation } from "../../platform/navigation/source-navigation-intent.ts";
import { describe, expect, it, vi } from "vitest";
import {
  onProjectFilesRevealRequest,
  setProjectFilesRevealRequest,
  takeProjectFilesRevealRequest,
} from "./project-files-reveal.ts";

describe("project files reveal request", () => {
  it("retains root identity and is consumed only by its project", () => {
    const listener = vi.fn();
    const stop = onProjectFilesRevealRequest(listener);
    setProjectFilesRevealRequest({
      projectId: "project-1",
      rootId: "root-1",
      path: "meta-packs",
      isDir: true,
    });
    expect(listener).toHaveBeenCalledOnce();
    expect(takeProjectFilesRevealRequest("project-other")).toBeNull();
    expect(takeProjectFilesRevealRequest("project-1")).toEqual({
      projectId: "project-1",
      rootId: "root-1",
      path: "meta-packs",
      isDir: true,
    });
    expect(takeProjectFilesRevealRequest("project-1")).toBeNull();
    stop();
  });
  it("discards a queued reveal superseded while its view was mounting", () => {
    setProjectFilesRevealRequest({ projectId: "p", rootId: "r", path: "older.ts", isDir: false });
    beginSourceNavigation();
    expect(takeProjectFilesRevealRequest("p")).toBeNull();
  });

});
