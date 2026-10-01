import type { FindingLedgerEntry } from "../../api/types.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import { addToChat } from "../../chat/composer/add-to-chat.ts";
import { resolveProjectFile } from "../../api/project-path.ts";
import { primaryLocation } from "../../lib/scan-display.ts";

/** Stages findings as a chat draft with file attachments without sending. */

export type FixWithAgentArgs = {
  projectId: string;
  roots: readonly ResolveProjectRoot[];
  entries: readonly FindingLedgerEntry[];
};

export function fixFindingsPrompt(entries: readonly FindingLedgerEntry[]): string {
  if (entries.length === 0) return "";
  const only = entries.length === 1 ? entries[0] : undefined;
  if (only) {
    const location = primaryLocation(only.finding);
    const where = location?.uri ? ` at ${location.uri}:${location.start_line ?? 1}` : "";
    return `Fix this security finding${where}: ${only.finding.message} (${only.finding.rule_id})`;
  }
  const rules = new Set(entries.map((entry) => entry.finding.rule_id).filter(Boolean));
  const subject =
    rules.size === 1
      ? `${entries.length} findings from ${[...rules][0]}`
      : `${entries.length} security findings across ${rules.size} rules`;
  const lines = entries.map((entry) => {
    const location = primaryLocation(entry.finding);
    const where = location?.uri ? `${location.uri}:${location.start_line ?? 1}` : "unknown location";
    return `- ${where} — ${entry.finding.message} (${entry.finding.rule_id})`;
  });
  return [`Fix ${subject}:`, ...lines].join("\n");
}

export type FixWithAgentResult = { ok: true } | { ok: false; reason: string };

export async function fixFindingsWithAgent(args: FixWithAgentArgs): Promise<FixWithAgentResult> {
  if (args.entries.length === 0) return { ok: false, reason: "Nothing selected." };
  const draft = fixFindingsPrompt(args.entries);

  // One attachment per file; the draft lists every line.
  const attached = new Set<string>();
  let staged = 0;
  let lastFailure = "";
  let draftPrefill: string | undefined = draft;
  for (const entry of args.entries) {
    const location = primaryLocation(entry.finding);
    const path = location?.uri?.trim();
    if (!path || attached.has(path)) continue;
    attached.add(path);
    // Finding uris are repo-relative.
    const resolved = resolveProjectFile({ roots: args.roots }, path);
    if ("error" in resolved) {
      lastFailure = `${path} is not under this project's roots.`;
      continue;
    }
    const result = await addToChat(
      {
        kind: "path-file",
        projectId: args.projectId,
        rootId: resolved.rootId,
        path,
        name: path.split("/").pop() ?? path,
        startLine: location?.start_line,
        endLine: location?.start_line,
      },
      { draftPrefill },
    );
    if (!result.ok) {
      lastFailure = result.reason;
      continue;
    }
    // The draft belongs to the message, not to each attachment.
    draftPrefill = undefined;
    staged += 1;
  }
  if (staged === 0) {
    return { ok: false, reason: lastFailure || "No finding named a file in this project." };
  }
  return { ok: true };
}
