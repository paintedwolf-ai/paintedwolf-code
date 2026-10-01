import { beforeEach, describe, expect, it, vi } from "vitest";
import { addFindingsToChat, type FindingChatContext } from "./add-findings-to-chat.ts";

const attach = vi.hoisted(() => vi.fn());
vi.mock("../../chat/composer/add-to-chat.ts", () => ({
  addTextAttachmentToChat: (...args: unknown[]) => attach(...args),
}));

function entry(fingerprint: string): FindingChatContext {
  return {
    state: "open",
    scanner_id: "opengrep-sast",
    last_scan_id: "scan-1",
    finding: {
      rule_id: "sql-concat",
      level: "high",
      message: "Unsafe query",
      locations: [{ uri: "query.go", start_line: 12, end_line: 15 }],
      fingerprints: { primary: fingerprint },
      tool: { driver_id: "opengrep-sast", name: "opengrep" },
    },
  };
}

describe("add findings to chat", () => {
  beforeEach(() => attach.mockReset().mockResolvedValue({ ok: true }));

  it("keeps every finding in one batch, including repeated files and missing locations", async () => {
    const entries = [entry("one"), entry("two"), entry("three")];
    entries[2]!.finding.locations = [];
    await addFindingsToChat("project-1", entries);
    expect(attach).toHaveBeenCalledTimes(1);
    const [projectId, body, name] = attach.mock.calls[0]!;
    expect(projectId).toBe("project-1");
    expect(name).toBe("security-findings.txt");
    const parsed = JSON.parse(body);
    expect(parsed.findings.map((item: FindingChatContext) => item.finding)).toEqual(entries.map((item) => item.finding));
    expect(parsed.findings[0]).toMatchObject({ state: "open", scanner_id: "opengrep-sast", scan_id: "scan-1" });
  });

  it("retains the historical run identity without inventing ledger state", async () => {
    await addFindingsToChat("project-1", [{ finding: entry("one").finding, last_scan_id: "older-scan" }]);
    const [, body, name] = attach.mock.calls[0]!;
    expect(name).toBe("security-finding.txt");
    expect(JSON.parse(body).findings[0]).toMatchObject({ scan_id: "older-scan" });
    expect(JSON.parse(body).findings[0]).not.toHaveProperty("state");
  });

  it.each(["primary", "secondary", "folder café 🐺"])("retains %s folder identity alongside relative finding paths", async (id) => {
    const root = { id, path: `/fixtures/${id}` };
    await addFindingsToChat("project-1", [entry("one")], root);
    const [, body] = attach.mock.calls[0]!;
    expect(JSON.parse(body)).toMatchObject({
      project_id: "project-1", root_id: id, root_path: root.path,
      findings: [{ finding: { locations: [{ uri: "query.go", start_line: 12 }] } }],
    });
  });

  it("does not open a destination for an empty selection", async () => {
    await expect(addFindingsToChat("project-1", [])).resolves.toMatchObject({ ok: false });
    expect(attach).not.toHaveBeenCalled();
  });
});
