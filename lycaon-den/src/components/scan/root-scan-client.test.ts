import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { rootScanClient } from "./root-scan-client.ts";

describe("folder scan authority", () => {
  it.each(["primary", "secondary", "folder café 🐺"])("binds every ledger read and action to %s", (rootId) => {
    const calls = {
      listCodeScans: vi.fn(), getProjectSecurity: vi.fn(), queryProjectFindings: vi.fn(),
      listProjectFindingIgnores: vi.fn(), createProjectFindingIgnore: vi.fn(), deleteProjectFindingIgnore: vi.fn(),
      exportProjectFindings: vi.fn(), startFullScan: vi.fn(), getCodeScan: vi.fn(),
    };
    const client = rootScanClient(stubClient(calls), rootId);
    void client.listCodeScans("project", { limit: 25, root_id: "wrong" });
    void client.getProjectSecurity("project");
    void client.queryProjectFindings("project", { text: "fixture" });
    void client.listProjectFindingIgnores("project");
    void client.createProjectFindingIgnore("project", { path: "fixture/**", reason: "reviewed" });
    void client.deleteProjectFindingIgnore("project", "entry");
    void client.exportProjectFindings("project", { format: "sarif" });
    void client.startFullScan("project", { scanner_ids: ["scanner"] });
    expect(calls.listCodeScans).toHaveBeenCalledWith("project", { limit: 25, root_id: rootId });
    expect(calls.getProjectSecurity).toHaveBeenCalledWith("project", rootId);
    expect(calls.queryProjectFindings).toHaveBeenCalledWith("project", { text: "fixture" }, rootId);
    expect(calls.listProjectFindingIgnores).toHaveBeenCalledWith("project", rootId);
    expect(calls.createProjectFindingIgnore).toHaveBeenCalledWith("project", { path: "fixture/**", reason: "reviewed" }, rootId);
    expect(calls.deleteProjectFindingIgnore).toHaveBeenCalledWith("project", "entry", rootId);
    expect(calls.exportProjectFindings).toHaveBeenCalledWith("project", { format: "sarif" }, rootId);
    expect(calls.startFullScan).toHaveBeenCalledWith("project", { scanner_ids: ["scanner"] }, rootId);
    expect(client.getCodeScan).toBe(calls.getCodeScan);
    expect(calls.getProjectSecurity).not.toBe(client.getProjectSecurity);
  });

  it("returns one client per folder so retained reads survive a re-render", () => {
    const base = stubClient();
    expect(rootScanClient(base, "primary")).toBe(rootScanClient(base, "primary"));
    expect(rootScanClient(base, "primary")).not.toBe(rootScanClient(base, "secondary"));
    expect(rootScanClient(stubClient(), "primary")).not.toBe(rootScanClient(base, "primary"));
  });
});
