import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { loadEditorAnnotations } from "./editor-annotations.ts";

describe("editor annotation scope", () => {
  it("keeps annotations for identical paths in different projects independent", async () => {
    const client = stubClient({
      getProjectSourceAttribution: vi.fn(async (projectId: string) => ({ head_sha256: projectId, intervals: [] })),
      listCodeScans: vi.fn(async (projectId: string) => ({ scans: [{ id: projectId + "-scan", status: "complete" }] })),
      queryCodeScan: vi.fn(async () => ({ findings: [] })),
    });
    const first = await loadEditorAnnotations(client, { projectId: "first", rootId: "root-a", path: "main.go", sessionId: "session-a" });
    const second = await loadEditorAnnotations(client, { projectId: "second", rootId: "root-b", path: "main.go", sessionId: "session-b" });
    expect(first.scanId).toBe("first-scan");
    expect(second.scanId).toBe("second-scan");
    expect(second.attribution.head_sha256).toBe("second");
    expect(client.getProjectSourceAttribution).toHaveBeenLastCalledWith("second", { rootId: "root-b", path: "main.go", sessionId: "session-b" });
  });

  it("retains attribution when the scan query fails and retries on the next presentation", async () => {
    const client = stubClient({
      getProjectSourceAttribution: vi.fn(async () => ({ head_sha256: "content", intervals: [] })),
      listCodeScans: vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue({ scans: [{ id: "recovered", status: "complete" }] }),
      queryCodeScan: vi.fn(async () => ({ findings: [] })),
    });
    const source = { projectId: "p", rootId: "r", path: "main.go" };
    const offline = await loadEditorAnnotations(client, source);
    expect(offline.attribution.head_sha256).toBe("content");
    expect(offline.scanId).toBe("");
    expect((await loadEditorAnnotations(client, source)).scanId).toBe("recovered");
  });
});
