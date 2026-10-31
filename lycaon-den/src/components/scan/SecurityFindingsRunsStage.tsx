import { Show } from "solid-js";
import type {
  CodeScan,
  FindingLedgerEntry,
  SecurityFinding,
  SecurityOverview,
} from "../../api/types.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { LEDGER_LIST_SURFACE, SECURITY_LIST_SURFACE } from "../../lib/scan-columns.ts";
import { commitListSort, listPanePrefs } from "../../list/list-layout.ts";
import { resolveListPaneSort } from "../../list/list-pane-model.ts";
import type { SortState } from "../../list/list-sort.ts";
import { anyPassSuperseded, ledgerEntryKey } from "../../lib/ledger-display.ts";
import { defaultFullScanSelection } from "../../lib/scan-coverage.ts";
import type { createDisplayedFullPass } from "./displayed-full-pass.ts";
import { IgnoreCatalogPanel } from "./IgnoreCatalogPanel.tsx";

type DisplayedFullPass = ReturnType<ReturnType<typeof createDisplayedFullPass>>;
import { IgnoreFindingPanel } from "./IgnoreFindingPanel.tsx";
import { LedgerFindingDetail } from "./LedgerFindingDetail.tsx";
import { LedgerFindingsList } from "./LedgerFindingsList.tsx";
import { RunFindingDetail } from "./RunFindingDetail.tsx";
import { RunFindingsList } from "./RunFindingsList.tsx";
import { SecurityCoverage } from "./SecurityCoverage.tsx";
import { SecurityEmptyState, type EmptyReason } from "./SecurityEmptyState.tsx";
import type { FindingChatContext } from "./add-findings-to-chat.ts";
import type { FindingLedgerModel } from "./create-finding-ledger.ts";

export type SecurityFindingsDetailPaneProps = {
  isLedger: boolean;
  selectedLedgerEntry: FindingLedgerEntry | null;
  selectedRunFinding: SecurityFinding | null;
  selectedScan: CodeScan | null;
  ledger: FindingLedgerModel;
  scannerLabels: Record<string, string>;
  repoRoot: string | undefined;
  addingToChat: boolean;
  onOpenRun: (scanId: string) => void;
  onHandOffToAgent: (entries: readonly FindingLedgerEntry[]) => void;
  onAttachFindings: (entries: readonly FindingChatContext[]) => void;
  onCloseLedgerDetail: () => void;
  onCloseRunDetail: () => void;
};

export function renderSecurityFindingsDetailPane(props: SecurityFindingsDetailPaneProps) {
  if (props.isLedger) {
    const entry = props.selectedLedgerEntry;
    return entry ? (
      <LedgerFindingDetail
        entry={entry}
        ledger={props.ledger}
        scannerLabels={props.scannerLabels}
        repoRoot={props.repoRoot}
        onOpenRun={props.onOpenRun}
        onFixWithAgent={() => props.onHandOffToAgent([entry])}
        onAddToChat={() => props.onAttachFindings([entry])}
        addingToChat={props.addingToChat}
        onClose={props.onCloseLedgerDetail}
      />
    ) : null;
  }
  const finding = props.selectedRunFinding;
  return finding ? (
    <RunFindingDetail
      finding={finding}
      onAddToChat={() =>
        props.onAttachFindings([
          {
            finding,
            last_scan_id: props.selectedScan?.id,
          },
        ])
      }
      addingToChat={props.addingToChat}
      scan={props.selectedScan}
      scannerLabels={props.scannerLabels}
      repoRoot={props.repoRoot}
      onClose={props.onCloseRunDetail}
    />
  ) : null;
}

export type SecurityFindingsRunsStageProps = {
  projectId: string | undefined;
  repoRoot: string | undefined;
  isLedger: boolean;
  catalogOpen: boolean;
  ledgerHeld: boolean;
  ledgerEmpty: boolean;
  emptyReason: EmptyReason;
  loadError: string | null | undefined;
  onRetryLoading: () => void;
  ledger: FindingLedgerModel;
  overview: SecurityOverview | null;
  displayedPass: DisplayedFullPass | null;
  newSince: boolean;
  onToggleNewSince: () => void;
  startingFullScan: boolean;
  onStartFullScan: (scannerIds: string[]) => Promise<void>;
  addingToChat: boolean;
  onAttachFindings: (entries: readonly FindingChatContext[]) => void;
  onHandOffToAgent: (entries: readonly FindingLedgerEntry[]) => void;
  ignoreOpen: boolean;
  setIgnoreOpen: (open: boolean | ((prev: boolean) => boolean)) => void;
  selectedScan: CodeScan | null;
  runFilteredFindings: SecurityFinding[];
  allFindingsCount: number;
  fixedFindings: SecurityFinding[];
  findingsWithoutHistory: number;
  runLoading: boolean;
  runShowLoading: boolean;
  runFiltersActive: boolean;
  runShowEmpty: boolean;
  onClearFilters: () => void;
  findingsPage: number;
  onPageChange: (page: number) => void;
  onRestartRunView: () => void;
  drillId: string | null;
  onSelectRunFinding: (fingerprint: string) => void;
  ledgerKey: string | null;
  onSelectLedgerKey: (key: string | null) => void;
  onListRef: (element: HTMLDivElement) => void;
};

export function SecurityFindingsRunsStage(props: SecurityFindingsRunsStageProps) {
  let ignoreAnchorEl: HTMLButtonElement | undefined;
  const runSort = (): SortState => resolveListPaneSort(listPanePrefs(SECURITY_LIST_SURFACE));

  return (
    <>
      <Show when={props.loadError}>
        <DenButton variant="link" onClick={props.onRetryLoading}>
          Retry loading
        </DenButton>
      </Show>

      <Show
        when={!(props.isLedger && props.catalogOpen)}
        fallback={
          <IgnoreCatalogPanel
            catalog={props.ledger.ignores.value() ?? null}
            repoRoot={props.repoRoot}
            pending={props.ledger.ignorePending()}
            onWithdraw={(entryId) => void props.ledger.removeIgnore(entryId)}
          />
        }
      >
        <Show when={!props.ledgerHeld}>
          <Show when={!props.ledgerEmpty}>
            <SecurityCoverage
              projectId={props.projectId}
              overview={props.overview}
              displayed={props.displayedPass}
              newSince={
                props.isLedger
                  ? props.ledger.introducedSince() !== undefined
                  : props.newSince
              }
              passSuperseded={anyPassSuperseded(props.overview)}
              onToggleNewSince={props.onToggleNewSince}
              onStart={props.onStartFullScan}
              starting={props.startingFullScan}
            />
          </Show>

          <Show when={props.ledgerEmpty}>
            <SecurityEmptyState
              reason={props.emptyReason}
              overview={props.overview}
              displayed={props.displayedPass}
              starting={props.startingFullScan}
              onStartFullScan={() =>
                props.onStartFullScan([...defaultFullScanSelection(props.overview)])
              }
              onClearFilters={props.onClearFilters}
            />
          </Show>

          <Show when={props.isLedger && props.ledger.selected().size > 0}>
            <div class="den-ledger-selection" data-testid="ledger-selection">
              <span class="den-ledger-selection__count">
                {props.ledger.selected().size} selected
              </span>
              <div class="den-ledger-selection__actions">
                <DenButton
                  variant="secondary"
                  compact
                  data-testid="ledger-selection-add-to-chat"
                  disabled={props.addingToChat}
                  onClick={() => props.onAttachFindings(props.ledger.selectedEntries())}
                >
                  Add to chat
                </DenButton>
                <DenButton
                  variant="primary"
                  compact
                  data-testid="ledger-selection-fix"
                  onClick={() => props.onHandOffToAgent(props.ledger.selectedEntries())}
                >
                  Fix with agent
                </DenButton>
                <DenButton
                  variant="secondary"
                  compact
                  data-testid="ledger-selection-ignore"
                  ref={(element: HTMLButtonElement) => {
                    ignoreAnchorEl = element;
                  }}
                  onClick={() => props.setIgnoreOpen((open) => !open)}
                >
                  Ignore…
                </DenButton>
                <DenButton
                  variant="ghost"
                  compact
                  data-testid="ledger-selection-clear"
                  onClick={() => props.ledger.clearSelection()}
                >
                  Clear
                </DenButton>
              </div>
              <Show when={props.ignoreOpen}>
                <IgnoreFindingPanel
                  entries={props.ledger.selectedEntries()}
                  path={props.ledger.ignores.value()?.path}
                  anchor={() => ignoreAnchorEl}
                  pending={props.ledger.ignorePending()}
                  onCommit={(entries) => {
                    void props.ledger.addIgnores(entries).then((ok) => {
                      if (ok) props.setIgnoreOpen(false);
                    });
                  }}
                  onDismiss={() => props.setIgnoreOpen(false)}
                />
              </Show>
            </div>
          </Show>

          <Show
            when={props.isLedger}
            fallback={
              <RunFindingsList
                scan={props.selectedScan}
                findings={props.runFilteredFindings}
                loadedCount={props.allFindingsCount}
                fixedFindings={props.fixedFindings}
                newSince={props.newSince}
                findingsWithoutHistory={props.findingsWithoutHistory}
                loading={props.runLoading}
                showLoading={props.runShowLoading}
                filtersActive={props.runFiltersActive}
                showEmpty={props.runShowEmpty}
                onClearFilters={props.onClearFilters}
                page={props.findingsPage}
                onPageChange={props.onPageChange}
                sort={runSort()}
                onSort={(state) => {
                  void commitListSort(SECURITY_LIST_SURFACE, state);
                  props.onRestartRunView();
                }}
                selectedFingerprint={props.drillId}
                onSelect={props.onSelectRunFinding}
                repoRoot={props.repoRoot}
                listRef={props.onListRef}
              />
            }
          >
            <LedgerFindingsList
              ledger={props.ledger}
              overview={props.overview}
              repoRoot={props.repoRoot}
              selectedKey={props.ledgerKey}
              onSelect={(entry) => {
                const key = ledgerEntryKey(entry);
                props.onSelectLedgerKey(props.ledgerKey === key ? null : key);
              }}
              listRef={props.onListRef}
            />
          </Show>
        </Show>
      </Show>
    </>
  );
}

export function securityDetailSurface(isLedger: boolean) {
  return isLedger ? LEDGER_LIST_SURFACE : SECURITY_LIST_SURFACE;
}
