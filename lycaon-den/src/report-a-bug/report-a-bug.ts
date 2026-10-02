import { ISSUES_URL } from "../../shared/brand.ts";
import type { BackendConnection } from "../platform/connection/backend.ts";
import { openAppLink } from "../platform/desktop/external-link.ts";
import {
  saveDiagnosticsBundle,
  type SaveBundleResult,
} from "../settings/system/diagnostics-export.ts";

export type ReportBugStep =
  | { kind: "explain"; error?: string }
  | { kind: "saving" }
  | { kind: "saved"; path: string };

export type ReportBugSave = (
  connection: BackendConnection,
) => Promise<SaveBundleResult>;

export type ReportBugOpenIssues = (url: string) => Promise<boolean>;

export function initialReportBugStep(): ReportBugStep {
  return { kind: "explain" };
}

export function applySaveResult(result: SaveBundleResult): ReportBugStep {
  switch (result.kind) {
    case "saved":
      return { kind: "saved", path: result.path };
    case "cancelled":
      return { kind: "explain" };
    case "failed":
      return { kind: "explain", error: result.detail };
  }
}

export async function saveReportBundle(
  connection: BackendConnection | null,
  save: ReportBugSave = saveDiagnosticsBundle,
): Promise<ReportBugStep> {
  if (!connection) {
    return {
      kind: "explain",
      error: "Not connected to the engine — start it and try again.",
    };
  }
  return applySaveResult(await save(connection));
}

export async function openIssuesPage(
  open: ReportBugOpenIssues = (url) => openAppLink(url),
): Promise<boolean> {
  return open(ISSUES_URL);
}
