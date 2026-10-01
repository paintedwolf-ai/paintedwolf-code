import type { FindingLedgerEntry, SecurityFinding } from "../../api/types.ts";
import { addTextAttachmentToChat } from "../../chat/composer/add-to-chat.ts";

/** Finding context can come from the project ledger or a historical run. */
export type FindingChatContext = {
  finding: SecurityFinding;
  scanner_id?: string;
  state?: FindingLedgerEntry["state"];
  last_scan_id?: string;
};

/** One attachment preserves every selected finding, including those without files. */
export async function addFindingsToChat(
  projectId: string,
  entries: readonly FindingChatContext[],
  root?: { id: string; path: string },
) {
  if (!entries.length) return { ok: false as const, reason: "Nothing selected." };
  return addTextAttachmentToChat(
    projectId,
    JSON.stringify({
      source: "Security scanner findings",
      project_id: projectId,
      root_id: root?.id,
      root_path: root?.path,
      findings: entries.map((entry) => ({
        finding: entry.finding,
        scanner_id: entry.scanner_id,
        state: entry.state,
        scan_id: entry.last_scan_id,
      })),
    }, null, 2),
    entries.length === 1 ? "security-finding.txt" : "security-findings.txt",
  );
}
