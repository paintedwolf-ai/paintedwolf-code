import { searchIssueNotes } from "../../search/search-status.ts";
import { For, Show, createSignal } from "solid-js";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import type {
  SearchReplaceFileOutcome,
  SearchReplaceFilePreview,
  SearchReplacePreviewResponse,
} from "../../api/types.ts";
import type { FileBatchReport } from "../../files/commands/file-mutations.ts";
import {
  countHunksByContext,
  replaceSelectionCounts,
  setHunksCheckedByContext,
  setReplaceFileCollapsed,
  skipReasonLabel,
  toggleReplaceFile,
  toggleReplaceHunk,
  type ReplaceSelection,
} from "../../search/search-replace-selection.ts";

export type ReplaceApplySummary = {
  appliedMatches: number;
  appliedFiles: number;
  skipped: SearchReplaceFileOutcome[];
  /** Source-ledger batch shared by every applied file — one-click revert. */
  batchId: string;
  applied: Array<{ root_id: string; path: string }>;
  renameFrom: string | null;
  replacement: string;
};

type Props = {
  preview: SearchReplacePreviewResponse | null;
  selection: ReplaceSelection | null;
  onSelectionChange: (next: ReplaceSelection) => void;
  applying: boolean;
  previewCurrent: boolean;
  preparing?: boolean;
  preparationNotice?: string;
  onCancelPreparation?: () => void;
  onRetryPreparation?: () => void;
  applySummary: ReplaceApplySummary | null;
  onApply: () => void;
  /** Explicit file selections remain available for limited previews. */
  onApplyFile: (fileIndex: number) => void;
  onCancel: () => void;
  onSearchAgain: () => void;
  applyAllDisabled: boolean;
  renameFrom?: string | null;
  onRevertBatch?: (summary: ReplaceApplySummary) => Promise<FileBatchReport>;
};

function hunkLineLabel(
  hunk: { line: number; end_line: number } | undefined,
): string {
  if (!hunk) return "";
  return hunk.end_line > hunk.line
    ? `L${hunk.line}–${hunk.end_line}`
    : `L${hunk.line}`;
}

export function SearchReplacePreview(props: Props) {
  const counts = () =>
    props.selection
      ? replaceSelectionCounts(props.selection)
      : { matches: 0, files: 0 };

  const files = (): SearchReplaceFilePreview[] => props.preview?.files ?? [];

  const verb = () => (props.renameFrom ? "Rename" : "Replace");
  const verbed = () => (props.renameFrom ? "Renamed" : "Replaced");

  const commentStringCount = () =>
    countHunksByContext(files(), "comment_or_string");

  const uncheckCommentStringHunks = () => {
    const selection = props.selection;
    if (!selection) return;
    props.onSelectionChange(
      setHunksCheckedByContext(
        selection,
        files(),
        "comment_or_string",
        false,
      ),
    );
  };

  return (
    <div class="den-search-replace" data-testid="search-replace-preview">
      <Show when={!props.previewCurrent && !props.applySummary && !props.applying}>
        <p class="den-search-status" role="status" data-testid="search-replace-updating">
          {props.preparationNotice || "Preparing an up-to-date preview. Nothing can be replaced yet."}
          <Show when={props.preparing && props.onCancelPreparation}>
            <DenButton variant="ghost" compact onClick={() => props.onCancelPreparation?.()}>Cancel preparation</DenButton>
          </Show>
          <Show when={!props.preparing && props.onRetryPreparation}>
            <DenButton variant="ghost" compact onClick={() => props.onRetryPreparation?.()}>Retry preview</DenButton>
          </Show>
        </p>
      </Show>
      <Show when={!props.applySummary && props.previewCurrent && props.preview?.state === "limited"}>
        <p class="den-search-status" role="status" data-testid="search-replace-coverage">
          {searchIssueNotes(props.preview?.issues)} Apply all is unavailable. Review and replace individual files, or refine the scope.
          <Show when={props.onRetryPreparation}>
            <DenButton variant="ghost" compact onClick={() => props.onRetryPreparation?.()}>Refresh preview</DenButton>
          </Show>
        </p>
      </Show>
      <Show when={props.renameFrom && !props.applySummary}>
        <p
          class="den-search-replace__rename-banner"
          data-testid="search-replace-rename-banner"
        >
          Rename <code>{props.renameFrom}</code> — whole word, case sensitive.
          Nothing is written until you rename.
        </p>
      </Show>

      <Show when={props.applySummary} keyed>
        {(summary) => {
          const [reverting, setReverting] = createSignal(false);
          const [revertReport, setRevertReport] = createSignal<FileBatchReport | null>(null);
          const revert = async () => {
            if (!props.onRevertBatch || reverting()) return;
            setReverting(true);
            try {
              setRevertReport(await props.onRevertBatch(summary));
            } finally {
              setReverting(false);
            }
          };
          return (
          <div
            class="den-search-replace__summary"
            data-testid="search-replace-summary"
          >
            <p class="den-search-replace__summary-title">
              <Show
                when={summary.renameFrom}
                fallback={
                  <>
                    {verbed()} {summary.appliedMatches} {summary.appliedMatches === 1 ? "match" : "matches"} in{" "}
                    {summary.appliedFiles} {summary.appliedFiles === 1 ? "file" : "files"}.
                  </>
                }
              >
                Renamed <code>{summary.renameFrom}</code> →{" "}
                <code>{summary.replacement}</code> — {summary.appliedMatches}{" "}
                {summary.appliedMatches === 1 ? "change" : "changes"} in {summary.appliedFiles} {summary.appliedFiles === 1 ? "file" : "files"}.
              </Show>
            </p>
            <Show when={summary.skipped.length > 0}>
              <div
                class="den-search-replace__skipped"
                data-testid="search-replace-skipped"
              >
                <p class="den-search-replace__skipped-title">
                  {summary.skipped.length} {summary.skipped.length === 1 ? "file" : "files"} skipped
                </p>
                <ul>
                  <For each={summary.skipped}>
                    {(row) => (
                      <li data-testid="search-replace-skipped-row">
                        <span class="den-search-replace__skipped-path">
                          {row.path}
                        </span>
                        <span class="den-search-replace__skipped-reason">
                          {skipReasonLabel(row.reason)}
                        </span>
                      </li>
                    )}
                  </For>
                </ul>
              </div>
            </Show>
            <div class="den-search-replace__summary-actions">
              <Show when={props.onRevertBatch && !revertReport()}>
                <DenButton
                  variant="secondary"
                  compact
                  data-testid="search-replace-revert"
                  disabled={reverting()}
                  onClick={() => void revert()}
                >
                  {reverting() ? "Reverting…" : "Revert"}
                </DenButton>
              </Show>
              <DenButton
                variant="secondary"
                compact
                data-testid="search-replace-again"
                onClick={() => props.onSearchAgain()}
              >
                Search again
              </DenButton>
            </div>
            <Show when={revertReport()}>
              {(report) => (
                <p
                  class="den-search-replace__revert-report"
                  data-testid="search-replace-revert-report"
                >
                  Reverted {report().ok} {report().ok === 1 ? "file" : "files"}
                  <Show when={report().skipped.length > 0}>
                    {" "}
                    · {report().skipped.length} skipped
                  </Show>
                  .
                </p>
              )}
            </Show>
            <Show when={revertReport()?.skipped.length}>
              <ul class="den-search-replace__skipped">
                <For each={revertReport()?.skipped}>
                  {(file) => <li>{file.path}: {file.reason}</li>}
                </For>
              </ul>
            </Show>
          </div>
          );
        }}
      </Show>

      <Show when={!props.applySummary && props.preview?.truncated}>
        <p
          class="den-search-status den-search-replace__truncated"
          data-testid="search-replace-truncated"
        >
          Preview truncated — select individual files to replace, or narrow the
          query. Apply all is disabled.
        </p>
      </Show>

      <Show when={!props.applySummary && files().length === 0 && props.preview && props.previewCurrent && props.preview.state === "ready"}>
        <div class="den-search__empty" data-testid="search-replace-empty">
          <p class="den-search__empty-title">No replaceable matches</p>
          <p class="den-search__empty-hint">
            Nothing matched in project source files, or matches were excluded.
          </p>
        </div>
      </Show>

      <Show when={!props.applySummary && props.selection} keyed>
        {(sel) => (
          <div
            class="den-search-replace__list"
            data-testid="search-replace-list"
          >
            <For each={sel.files}>
              {(file, fileIndex) => {
                const previewFile = () => files()[fileIndex()];
                return (
                  <div
                    class="den-search-replace__file"
                    data-testid="search-replace-file"
                  >
                    <div class="den-search-replace__file-row">
                      <button
                        type="button"
                        class="den-search-replace__chevron"
                        aria-expanded={!file.collapsed}
                        aria-label={
                          file.collapsed ? "Expand file" : "Collapse file"
                        }
                        data-testid="search-replace-collapse"
                        onClick={() =>
                          props.onSelectionChange(
                            setReplaceFileCollapsed(
                              sel,
                              fileIndex(),
                              !file.collapsed,
                            ),
                          )
                        }
                      >
                        {file.collapsed ? "▸" : "▾"}
                      </button>
                      <label class="den-search-replace__check">
                        <DenCheckboxControl
                          checked={file.checked}
                          data-testid="search-replace-file-check"
                          onChange={(e) =>
                            props.onSelectionChange(
                              toggleReplaceFile(
                                sel,
                                fileIndex(),
                                e.currentTarget.checked,
                              ),
                            )
                          }
                        />
                        <span class="den-search-replace__path">{file.path}</span>
                        <span class="den-search-replace__count">
                          {file.hunks.filter(Boolean).length}/
                          {file.hunks.length}
                        </span>
                      </label>
                      <Show when={props.applyAllDisabled}>
                        <DenButton
                          variant="secondary"
                          compact
                          data-testid="search-replace-file-apply"
                          disabled={
                            props.applying ||
                            !props.previewCurrent ||
                            file.hunks.filter(Boolean).length === 0
                          }
                          onClick={() => props.onApplyFile(fileIndex())}
                        >
                          {verb()} file
                        </DenButton>
                      </Show>
                    </div>
                    <Show when={!file.collapsed}>
                      <ul class="den-search-replace__hunks">
                        <For each={file.hunks}>
                          {(on, hunkIndex) => {
                            const hunk = () =>
                              previewFile()?.hunks[hunkIndex()];
                            return (
                              <li
                                class="den-search-replace__hunk"
                                data-testid="search-replace-hunk"
                              >
                                <label class="den-search-replace__check">
                                  <DenCheckboxControl
                                    checked={on}
                                    data-testid="search-replace-hunk-check"
                                    onChange={(e) =>
                                      props.onSelectionChange(
                                        toggleReplaceHunk(
                                          sel,
                                          fileIndex(),
                                          hunkIndex(),
                                          e.currentTarget.checked,
                                        ),
                                      )
                                    }
                                  />
                                  <span class="den-search-replace__line">
                                    {hunkLineLabel(hunk())}
                                  </span>
                                  <Show
                                    when={
                                      hunk()?.context === "comment_or_string"
                                    }
                                  >
                                    <span
                                      class="den-search-replace__context-chip den-status-mark"
                                      data-testid="search-replace-context-chip"
                                    >
                                      comment/string
                                    </span>
                                  </Show>
                                </label>
                                <div class="den-search-replace__diff">
                                  <div class="den-search-replace__before">
                                    {hunk()?.before ?? ""}
                                  </div>
                                  <div class="den-search-replace__after">
                                    {hunk()?.after ?? ""}
                                  </div>
                                </div>
                              </li>
                            );
                          }}
                        </For>
                      </ul>
                    </Show>
                  </div>
                );
              }}
            </For>
          </div>
        )}
      </Show>

      <Show when={!props.applySummary}>
        <div class="den-search-replace__footer" data-testid="search-replace-footer">
          <DenButton
            variant="primary"
            data-testid="search-replace-apply"
            disabled={
              props.applying ||
              !props.previewCurrent ||
              props.applyAllDisabled ||
              counts().matches === 0
            }
            onClick={() => props.onApply()}
          >
            {props.applying
              ? `${verb().replace(/e$/, "")}ing…`
              : `${verb()} ${counts().matches} ${
                  props.renameFrom
                    ? counts().matches === 1 ? "change" : "changes"
                    : counts().matches === 1 ? "match" : "matches"
                } in ${counts().files} ${counts().files === 1 ? "file" : "files"}`}
          </DenButton>
          <Show when={commentStringCount() > 0 && props.selection}>
            <DenButton
              variant="secondary"
              compact
              data-testid="search-replace-uncheck-comments"
              disabled={props.applying}
              onClick={uncheckCommentStringHunks}
            >
              Uncheck comment &amp; string matches ({commentStringCount()})
            </DenButton>
          </Show>
          <DenButton
            variant="ghost"
            data-testid="search-replace-cancel"
            disabled={props.applying}
            onClick={() => props.onCancel()}
          >
            Cancel
          </DenButton>
        </div>
      </Show>
    </div>
  );
}
