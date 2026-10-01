import { createEffect, createMemo, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { CodeScanEvent } from "../../api/types.ts";
import { SCANS_LIVE_REFRESH_MS, SCANS_LIVE_REFRESH_MAX_MS } from "../../lib/scan-display.ts";
import { FINDINGS_QUERY_LIMIT } from "../../lib/scan-findings-table.ts";
import { createAdaptivePoller } from "../../store/adaptive-poller.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";

/** A selected run stays observable even when it leaves the history page. */
export function createScanDetail(options: {
  client: () => LycaonClient | undefined;
  projectId: () => string | undefined;
  scanId: () => string | null;
  liveScan: () => CodeScanEvent | undefined;
  fixedSince: () => string | undefined;
}) {
  const live = useResidentLive();
  const summary = createSurfaceQuery({
    name: "scan-summary",
    source: () => {
      const client = options.client(), projectId = options.projectId(), scanId = options.scanId();
      return client && projectId && scanId ? { client, projectId, scanId, key: `${projectId}:${scanId}` } : null;
    },
    load: ({ client, projectId, scanId }) => client.getCodeScan(projectId, scanId, "summary"),
    required: false,
  });
  const scan = () => summary.value() ?? null;
  const active = () => scan()?.status === "pending" || scan()?.status === "running";
  const resultSource = createMemo(() => {
    const client = options.client(), projectId = options.projectId(), current = scan();
    const since = options.fixedSince();
    if (!client || !projectId || current?.status !== "complete") return null;
    const scope = `${projectId}:${current.id}`;
    return { client, projectId, scanId: current.id, since, scope,
      key: JSON.stringify([scope, current.finding_set_id, current.completed_at, since]) };
  }, undefined, { equals: (before, after) => before?.client === after?.client && before?.key === after?.key });
  const results = createSurfaceQuery({
    name: "scan-results",
    source: resultSource,
    scope: ({ scope }) => scope,
    load: ({ client, projectId, scanId, since }) => client.queryCodeScan(projectId, scanId, {
      limit: FINDINGS_QUERY_LIMIT,
      ...(since ? { fixed_since_at: since } : {}),
    }),
    required: false,
  });
  const refresh = async () => {
    await summary.refresh();
    await results.refresh();
  };
  const poll = createAdaptivePoller(async () => { await summary.refresh(); }, SCANS_LIVE_REFRESH_MS, SCANS_LIVE_REFRESH_MAX_MS);
  onCleanup(poll.cancel);
  createEffect(() => poll.setEnabled(live() && active()));
  createEffect(() => {
    const event = options.liveScan();
    if (!live() || event?.scan_id !== options.scanId()) return;
    untrack(() => { void summary.refresh(); });
  });
  return {
    summary, results, scan, active, refresh,
    findings: () => results.value()?.findings ?? [],
    fixedFindings: () => options.fixedSince() && results.displayed()?.source.since === options.fixedSince()
      ? results.value()?.fixed_findings ?? [] : [],
  };
}
