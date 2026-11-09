import { setDiagnostics, type Action, type Diagnostic } from "@codemirror/lint";
import { StateEffect, StateField, type EditorState, type Extension } from "@codemirror/state";
import { EditorView, showTooltip, type Tooltip } from "@codemirror/view";
import { sourceToDisplayOffset } from "./pretty-map.ts";
import {
  formatRecoverySelectionSnapshot,
  type PrettyDocumentRecoveryState,
  type PrettyPendingConflictRecord,
  type PrettyPendingStaleEdit,
} from "./pretty-replica-recovery.ts";
import type { PrettyProjectionResult } from "./pretty-types.ts";

export const refreshPrettyRecoveryUi = StateEffect.define<number>();

export interface PrettyRecoveryUiController {
  getRecoveryState(): PrettyDocumentRecoveryState;
  getCurrentProjection(): PrettyProjectionResult;
  copyPendingRecovery(targetId?: string): { readonly displayText: string; readonly rawText: string } | null;
  copyUnreadableRecoveryPayload(): string | null;
  setRecoveryFeedback(message: string): void;
  resolvePendingConflict(resolution: {
    readonly action: "apply" | "discard-intention" | "discard-conflict";
    readonly conflictId?: string;
    readonly order?: number;
    readonly discardRemainingIntentions?: boolean;
  }): {
    readonly ok: boolean;
    readonly status: string;
  };
  discardPendingStaleEdit(staleEditId?: string): boolean;
  selectRecoveryRecord(recordId: string): void;
}

export function summarizePreviewText(text: string, maxChars = 32): string {
  const singleLine = text.replace(/\r?\n/g, "↵");
  if (singleLine.length <= maxChars) return singleLine;
  return `${singleLine.slice(0, maxChars)}… (${text.length} chars)`;
}

type RecoveryItemEntry =
  | {
      readonly kind: "conflict";
      readonly tier: "durable" | "overflow";
      readonly id: string;
      readonly record: PrettyPendingConflictRecord;
    }
  | {
      readonly kind: "stale-edit";
      readonly tier: "durable" | "overflow";
      readonly id: string;
      readonly record: PrettyPendingStaleEdit;
    };

function collectRecoveryEntries(recovery: PrettyDocumentRecoveryState): RecoveryItemEntry[] {
  const entries: RecoveryItemEntry[] = [];
  for (const c of recovery.pendingConflicts) {
    entries.push({ kind: "conflict", tier: "durable", id: c.conflictId, record: c });
  }
  for (const s of recovery.pendingStaleEdits) {
    entries.push({ kind: "stale-edit", tier: "durable", id: s.staleEditId, record: s });
  }
  if (recovery.capacityOverflowRecord) {
    const ov = recovery.capacityOverflowRecord;
    if (ov.kind === "conflict") {
      entries.push({ kind: "conflict", tier: "overflow", id: ov.record.conflictId, record: ov.record });
    } else {
      entries.push({ kind: "stale-edit", tier: "overflow", id: ov.record.staleEditId, record: ov.record });
    }
  }
  return entries;
}

function resolveEntryDisplayRange(
  entry: RecoveryItemEntry,
  projection: PrettyProjectionResult,
  docLength: number,
): { from: number; to: number } {
  let rawFrom = 0;
  let rawTo = 0;
  if (entry.kind === "conflict") {
    const range = entry.record.affectedRawRanges[0];
    const intention = entry.record.intentions[0];
    rawFrom = intention?.rawFrom ?? range?.srcFrom ?? 0;
    rawTo = intention?.rawTo ?? range?.srcTo ?? rawFrom;
  } else {
    rawFrom = entry.record.rebasedCursorOffset;
    rawTo = entry.record.rebasedCursorOffset;
  }
  const dispFrom = Math.max(
    0,
    Math.min(docLength, sourceToDisplayOffset(projection.sourceMap, rawFrom, "right")),
  );
  const dispTo = Math.max(
    dispFrom,
    Math.min(docLength, sourceToDisplayOffset(projection.sourceMap, rawTo, "right")),
  );
  return { from: dispFrom, to: Math.min(docLength, Math.max(dispTo, dispFrom + 1)) };
}

function tryWriteClipboard(text: string, controller: PrettyRecoveryUiController): void {
  try {
    const nav = typeof navigator !== "undefined" ? navigator : undefined;
    if (nav?.clipboard && typeof nav.clipboard.writeText === "function") {
      void Promise.resolve(nav.clipboard.writeText(text)).then(
        () => {
          controller.setRecoveryFeedback("Copied retained edit to clipboard");
        },
        () => {
          controller.setRecoveryFeedback(
            "Selected retained edit in text box — press Copy shortcut to copy",
          );
        },
      );
      return;
    }
  } catch {
    // Fall through to text box selection feedback when clipboard access fails.
  }
  controller.setRecoveryFeedback(
    "Selected retained edit in text box — press Copy shortcut to copy",
  );
}

function buildTooltipDom(
  view: EditorView,
  controller: PrettyRecoveryUiController,
): HTMLElement {
  const recovery = controller.getRecoveryState();
  const entries = collectRecoveryEntries(recovery);
  const activeEntry =
    entries.find((e) => e.id === recovery.selectedRecoveryRecordId) ?? entries[0] ?? null;

  const root = view.dom.ownerDocument.createElement("div");
  root.className = "cm-pretty-recovery-tooltip";
  root.dataset.testid = "pretty-recovery-tooltip";
  root.setAttribute("role", "region");
  root.setAttribute("aria-label", "Pretty edit recovery");
  root.tabIndex = -1;
  root.style.maxHeight = "240px";
  root.style.overflowY = "auto";
  root.style.maxWidth = "520px";
  root.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      view.contentDOM.focus();
    }
  });

  const statusMessages: string[] = [];
  if (recovery.lastStorageError) {
    statusMessages.push(
      `Recovery storage error (${recovery.lastStorageError.message}): retained edits are held in memory only and will be lost on close or reload unless copied or inserted.`,
    );
  }
  if (recovery.capacityOverflowRecord !== null) {
    statusMessages.push(
      "Recovery storage limit reached: the latest retained edit is held in memory only and will be lost on close or reload unless copied or inserted. Showing original source lines until retained edits are resolved.",
    );
  }
  if (statusMessages.length > 0) {
    const statusEl = view.dom.ownerDocument.createElement("div");
    statusEl.dataset.testid = "pretty-recovery-status";
    statusEl.setAttribute("role", "alert");
    statusEl.textContent = statusMessages.join(" ");
    root.appendChild(statusEl);
  }

  if (recovery.lastCapacityRejectionMessage) {
    const capAlert = view.dom.ownerDocument.createElement("div");
    capAlert.dataset.testid = "pretty-recovery-capacity-alert";
    capAlert.setAttribute("role", "alert");
    capAlert.textContent = recovery.lastCapacityRejectionMessage;
    root.appendChild(capAlert);
  }

  if (recovery.lastRecoveryFeedback) {
    const feedbackEl = view.dom.ownerDocument.createElement("div");
    feedbackEl.dataset.testid = "pretty-recovery-feedback";
    feedbackEl.setAttribute("role", "status");
    feedbackEl.textContent = recovery.lastRecoveryFeedback;
    root.appendChild(feedbackEl);
  }

  const exportArea = view.dom.ownerDocument.createElement("textarea");
  exportArea.dataset.testid = "pretty-recovery-export-text";
  exportArea.readOnly = true;
  exportArea.rows = 2;
  exportArea.setAttribute("aria-label", "Retained edit export text");

  if (recovery.unreadableRawPayload) {
    exportArea.value = recovery.unreadableRawPayload;
    const copyUnreadableBtn = view.dom.ownerDocument.createElement("button");
    copyUnreadableBtn.type = "button";
    copyUnreadableBtn.dataset.testid = "pretty-recovery-copy-unreadable";
    copyUnreadableBtn.textContent = "Copy unreadable recovery data";
    copyUnreadableBtn.addEventListener("click", () => {
      const payload = controller.copyUnreadableRecoveryPayload();
      if (payload) {
        exportArea.value = payload;
        exportArea.focus();
        exportArea.select();
        tryWriteClipboard(payload, controller);
      }
    });
    root.appendChild(copyUnreadableBtn);
  }

  if (entries.length > 0 && activeEntry) {
    const recordsBar = view.dom.ownerDocument.createElement("div");
    recordsBar.dataset.testid = "pretty-recovery-records";
    for (const entry of entries) {
      const selectBtn = view.dom.ownerDocument.createElement("button");
      selectBtn.type = "button";
      selectBtn.dataset.testid = `pretty-recovery-select-${entry.id}`;
      selectBtn.dataset.recordId = entry.id;
      selectBtn.dataset.recordTier = entry.tier;
      selectBtn.setAttribute("aria-pressed", String(entry.id === activeEntry.id));
      const kindLabel = entry.kind === "conflict" ? "Multi-cursor edit" : "Concurrent edit";
      const preview = summarizePreviewText(
        entry.record.intentions.map((i) => i.insert).join(", "),
        32,
      );
      selectBtn.textContent =
        entry.tier === "overflow"
          ? `In memory only (${kindLabel}): ${preview}`
          : `Saved (${kindLabel}): ${preview}`;
      selectBtn.addEventListener("click", () => {
        controller.selectRecoveryRecord(entry.id);
      });
      recordsBar.appendChild(selectBtn);
    }
    root.appendChild(recordsBar);

    const recordDetail = view.dom.ownerDocument.createElement("div");
    recordDetail.dataset.testid = "pretty-recovery-active-record";
    recordDetail.dataset.recordId = activeEntry.id;
    recordDetail.dataset.recordKind = activeEntry.kind;
    recordDetail.dataset.recordTier = activeEntry.tier;

    const snap = formatRecoverySelectionSnapshot(activeEntry.record);
    exportArea.value = `${snap.displayText}\n${snap.rawText}`;

    const copyBtn = view.dom.ownerDocument.createElement("button");
    copyBtn.type = "button";
    copyBtn.dataset.testid = "pretty-recovery-copy";
    copyBtn.textContent = "Copy retained edit";
    copyBtn.addEventListener("click", () => {
      const copied = controller.copyPendingRecovery(activeEntry.id);
      if (copied) {
        exportArea.value = `${copied.displayText}\n${copied.rawText}`;
        exportArea.focus();
        exportArea.select();
        tryWriteClipboard(copied.displayText, controller);
      }
    });
    recordDetail.appendChild(copyBtn);

    if (activeEntry.kind === "conflict") {
      for (const intention of activeEntry.record.intentions) {
        const previewSnippet = summarizePreviewText(intention.insert, 24);
        const applyBtn = view.dom.ownerDocument.createElement("button");
        applyBtn.type = "button";
        applyBtn.dataset.testid = `pretty-recovery-apply-${intention.order}`;
        applyBtn.textContent = `Insert #${intention.order} (${previewSnippet})`;
        applyBtn.addEventListener("click", () => {
          controller.resolvePendingConflict({
            action: "apply",
            conflictId: activeEntry.id,
            order: intention.order,
          });
        });
        recordDetail.appendChild(applyBtn);

        const applyOnlyBtn = view.dom.ownerDocument.createElement("button");
        applyOnlyBtn.type = "button";
        applyOnlyBtn.dataset.testid = `pretty-recovery-apply-only-${intention.order}`;
        applyOnlyBtn.textContent = `Insert #${intention.order} only`;
        applyOnlyBtn.addEventListener("click", () => {
          controller.resolvePendingConflict({
            action: "apply",
            conflictId: activeEntry.id,
            order: intention.order,
            discardRemainingIntentions: true,
          });
        });
        recordDetail.appendChild(applyOnlyBtn);

        const discardIntentionBtn = view.dom.ownerDocument.createElement("button");
        discardIntentionBtn.type = "button";
        discardIntentionBtn.dataset.testid = `pretty-recovery-discard-intention-${intention.order}`;
        discardIntentionBtn.textContent = `Dismiss #${intention.order}`;
        discardIntentionBtn.addEventListener("click", () => {
          controller.resolvePendingConflict({
            action: "discard-intention",
            conflictId: activeEntry.id,
            order: intention.order,
          });
        });
        recordDetail.appendChild(discardIntentionBtn);
      }
    }

    const dismissBtn = view.dom.ownerDocument.createElement("button");
    dismissBtn.type = "button";
    dismissBtn.dataset.testid = "pretty-recovery-dismiss";
    dismissBtn.textContent = "Dismiss retained edit";
    dismissBtn.addEventListener("click", () => {
      if (activeEntry.kind === "conflict") {
        controller.resolvePendingConflict({
          action: "discard-conflict",
          conflictId: activeEntry.id,
        });
      } else {
        controller.discardPendingStaleEdit(activeEntry.id);
      }
    });
    recordDetail.appendChild(dismissBtn);

    root.appendChild(recordDetail);
  }

  root.appendChild(exportArea);
  return root;
}

function computeRecoveryTooltips(
  state: EditorState,
  controller: PrettyRecoveryUiController,
): readonly Tooltip[] {
  const recovery = controller.getRecoveryState();
  const entries = collectRecoveryEntries(recovery);
  const hasVisibleAlert =
    entries.length > 0 ||
    recovery.lastStorageError !== null ||
    recovery.unreadableRawPayload !== null ||
    recovery.lastCapacityRejectionMessage !== null ||
    recovery.lastRecoveryFeedback !== null;
  if (!hasVisibleAlert) return [];

  const activeEntry =
    entries.find((e) => e.id === recovery.selectedRecoveryRecordId) ?? entries[0] ?? null;
  const pos = activeEntry
    ? resolveEntryDisplayRange(
        activeEntry,
        controller.getCurrentProjection(),
        state.doc.length,
      ).from
    : Math.min(state.selection.main.head, state.doc.length);

  return [
    {
      pos,
      above: true,
      strictSide: false,
      arrow: false,
      create: (view) => {
        const container = view.dom.ownerDocument.createElement("div");
        const render = () => {
          container.replaceChildren(buildTooltipDom(view, controller));
        };
        render();
        return {
          dom: container,
          update: () => render(),
        };
      },
    },
  ];
}

export function buildPrettyRecoveryDiagnostics(
  state: EditorState,
  controller: PrettyRecoveryUiController,
): Diagnostic[] {
  const recovery = controller.getRecoveryState();
  const projection = controller.getCurrentProjection();
  const entries = collectRecoveryEntries(recovery);
  const diagnostics: Diagnostic[] = [];

  for (const entry of entries) {
    const range = resolveEntryDisplayRange(entry, projection, state.doc.length);
    const actions: Action[] = [
      {
        name: "Copy retained edit",
        apply: () => {
          const copied = controller.copyPendingRecovery(entry.id);
          if (copied) tryWriteClipboard(copied.displayText, controller);
        },
      },
    ];
    if (entry.kind === "conflict") {
      for (const intention of entry.record.intentions) {
        actions.push({
          name: `Insert #${intention.order}`,
          apply: () => {
            controller.resolvePendingConflict({
              action: "apply",
              conflictId: entry.id,
              order: intention.order,
            });
          },
        });
        actions.push({
          name: `Dismiss #${intention.order}`,
          apply: () => {
            controller.resolvePendingConflict({
              action: "discard-intention",
              conflictId: entry.id,
              order: intention.order,
            });
          },
        });
      }
      actions.push({
        name: "Dismiss retained edit",
        apply: () => {
          controller.resolvePendingConflict({
            action: "discard-conflict",
            conflictId: entry.id,
          });
        },
      });
    } else {
      actions.push({
        name: "Dismiss retained edit",
        apply: () => {
          controller.discardPendingStaleEdit(entry.id);
        },
      });
    }
    diagnostics.push({
      from: range.from,
      to: range.to,
      severity: entry.tier === "overflow" ? "error" : "warning",
      source: "pretty-recovery",
      message:
        entry.kind === "conflict"
          ? "Retained multi-cursor edit needs review before inserting"
          : "Retained edit needs review after concurrent document update",
      actions,
    });
  }
  return diagnostics;
}

export function createPrettyRecoveryUiExtension(
  controller: PrettyRecoveryUiController,
): Extension {
  const tooltipField = StateField.define<readonly Tooltip[]>({
    create: (state) => computeRecoveryTooltips(state, controller),
    update: (value, tr) => {
      let shouldRecompute = tr.docChanged;
      for (const e of tr.effects) {
        if (e.is(refreshPrettyRecoveryUi)) shouldRecompute = true;
      }
      return shouldRecompute ? computeRecoveryTooltips(tr.state, controller) : value;
    },
    provide: (field) => showTooltip.computeN([field], (state) => state.field(field)),
  });
  return [tooltipField];
}

export function syncPrettyRecoveryUiAndDiagnostics(
  view: EditorView | undefined,
  controller: PrettyRecoveryUiController,
  seq: number,
): void {
  if (!view) return;
  const diagSpec = setDiagnostics(
    view.state,
    buildPrettyRecoveryDiagnostics(view.state, controller),
  );
  view.dispatch({
    ...diagSpec,
    effects: [
      ...(Array.isArray(diagSpec.effects)
        ? diagSpec.effects
        : diagSpec.effects
          ? [diagSpec.effects]
          : []),
      refreshPrettyRecoveryUi.of(seq),
    ],
  });
}
