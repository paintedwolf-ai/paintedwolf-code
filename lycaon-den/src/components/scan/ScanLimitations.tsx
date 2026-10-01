import { For, Show, createSignal, createUniqueId, createEffect, on } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { CodeScan, ScanWarningKind } from "../../api/types.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { formatLocation, scanHasLimitations } from "../../lib/scan-display.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { TablePager } from "../list/TablePager.tsx";
import { paginateSlice } from "../../list/pagination.ts";

const labels: Record<ScanWarningKind, string> = {
  rule_parse_error: "Rules could not be loaded",
  file_partial_parse: "Source could not be fully parsed",
  file_partial_semantics: "Some language constructs could not be analyzed",
  target_unscanned: "Targets could not be scanned",
  source_moved: "Files changed while the scan ran",
};

export function ScanLimitations(props: {
  scan: CodeScan;
  client?: Pick<LycaonClient, "getCodeScan">;
  projectId: string | undefined;
  repoRoot?: string;
}) {
  const detailsId = createUniqueId();
  let trigger: HTMLButtonElement | undefined;
  const [expanded, setExpanded] = createSignal(false);
  const [page, setPage] = createSignal(0);
  const query = createSurfaceQuery({
    name: "scan-limitations",
    source: () => {
      const projectId = props.projectId?.trim();
      return expanded() && props.client && projectId
        ? { key: JSON.stringify([projectId, props.scan.id]), projectId, scanId: props.scan.id, client: props.client }
        : null;
    },
    load: ({ projectId, scanId, client }) => client.getCodeScan(projectId, scanId, "full"),
    required: false,
  });
  const warnings = () => query.value()?.warnings ?? props.scan.warnings ?? [];
  const pages = () => paginateSlice(warnings(), page(), 20);
  const summaries = () => props.scan.warning_summary ?? [];
  const issueCount = () => summaries().reduce((total, summary) => total + summary.count, 0) || warnings().length;
  const close = (refocus = false) => {
    setExpanded(false);
    if (refocus) queueMicrotask(() => trigger?.focus());
  };
  createEffect(on(() => props.scan.id, () => { setPage(0); close(); }, { defer: true }));
  return (
    <Show when={scanHasLimitations(props.scan)}>
      <DenButton variant="ghost" compact class="den-scans-issues-trigger" data-testid="scans-issues-trigger"
        ref={trigger} aria-haspopup="dialog" aria-expanded={expanded()} aria-controls={expanded() ? detailsId : undefined}
        onClick={() => { setPage(0); setExpanded(!expanded()); }}>
        Analysis issues<Show when={issueCount() > 0}> · {issueCount()}</Show>
      </DenButton>
      <Show when={expanded()}>
        <AnchoredSurface id={detailsId} anchor={() => trigger} preferredSide="bottom" align="end"
          class="den-scans-issues-popover" role="dialog" ariaLabel="Analysis issues" overflow="hidden"
          testId="scans-limitations" onDismiss={() => close()} onEscape={() => close(true)}>
          <Scrollport class="den-scans-issues-popover__scroll" contentClass="den-scans-issues-popover__content"
            viewport={{ tabIndex: -1 }} viewportRef={(viewport) => queueMicrotask(() => viewport.focus())}>
          <div class="den-scans-issues-heading">
            <h3>Analysis issues</h3>
            <DenButton variant="ghost" compact aria-label="Close analysis issues" onClick={() => close(true)}>Close</DenButton>
          </div>
          <p>{props.scan.coverage_status === "unavailable"
            ? "Analysis coverage is unavailable for this run. Any available findings remain in the results."
            : props.scan.coverage_status === "bounded"
              ? "Some directories were too large for the source budget and were not analyzed. Results cover every file within the budget."
              : "Some code could not be fully analyzed. Results cover the code the scanner could analyze."}</p>
          <ul class="den-scans-limitations-summary">
            <For each={summaries()}>{(summary) => <li>
              {labels[summary.kind]}
              <span class="den-scans-limitations-count">{summary.count} {summary.count === 1 ? "diagnostic" : "diagnostics"}
                <Show when={summary.files > 0}> · {summary.files} {summary.files === 1 ? "file" : "files"}</Show>
                <Show when={summary.rules > 0}> · {summary.rules} {summary.rules === 1 ? "rule" : "rules"}</Show>
              </span>
            </li>}</For>
          </ul>
          <div class="den-scans-diagnostics">
            <Show when={query.showLoading()}><p role="status">Loading diagnostics…</p></Show>
            <Show when={query.error()}>
              <p role="alert">Could not load diagnostics. Available findings are unaffected.</p>
              <DenButton variant="link" onClick={() => void query.refresh()}>Retry loading diagnostics</DenButton>
            </Show>
            <ul class="den-scans-diagnostics-list">
              <For each={pages().slice}>{(warning) => <li>
                <Show when={warning.file} fallback={<span>{labels[warning.kind]}</span>}>
                  <span class="den-scans-diagnostic-location">{formatLocation({ uri: warning.file ?? "", start_line: warning.start_line }, props.repoRoot)}
                    <Show when={warning.start_line && warning.start_column}>:{warning.start_column}</Show>
                  </span>
                  <span>{labels[warning.kind]}</span>
                </Show>
                <Show when={warning.rule_id}><code>{warning.rule_id}</code></Show>
                <Show when={warning.construct}><code>{warning.construct}</code></Show>
              </li>}</For>
            </ul>
            <Show when={!query.loading() && !query.error() && warnings().length === 0}>
              <p>No diagnostic locations were provided by the scanner.</p>
            </Show>
            <Show when={pages().pageCount > 1}>
              <TablePager page={pages().page} pageSize={20} total={warnings().length} onPageChange={setPage} ariaLabel="Diagnostic locations" />
            </Show>
          </div>
          </Scrollport>
        </AnchoredSurface>
      </Show>
    </Show>
  );
}
