import { isolateHistory } from "@codemirror/commands";
import { EditorSelection, EditorState, type Facet, Prec, StateField, Transaction, type Extension, type TransactionSpec } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import type * as Y from "yjs";
import { editorHistory } from "../editor/editor-history.ts";
import { documentDeltaChanges, documentHistoryKey, documentRemoteChange, documentSynchronization } from "../../../files/documents/document-editor-binding.ts";
import type { DocumentStorageError } from "../../../files/documents/document-outbox.ts";
import type { DocumentReplica } from "../../../files/documents/document-replica.ts";
import { gotoLineTargetFacet, type GotoLineTarget } from "../../../find/goto-line-controller.ts";
import { isImeComposition } from "../../../platform/interaction/ime-composition.ts";
import { shortcutPlatform } from "../../../shortcuts/platform.ts";
import { extractCopySelection, mapDisplayEditToSource } from "./pretty-edit-mapper.ts";
import { displayOffsetToSourceLineCol, displayToSourceOffset, sourceLineColToDisplay, sourceToDisplayOffset } from "./pretty-map.ts";
import { advanceProjectionForComposeFrame, PrettyRevisionDeltaRingBuffer, rebaseChangesOverDeltaChain } from "./pretty-replica-rebase.ts";
import {
  appendMultiCursorConflictToRecovery, appendStaleEditToRecovery, computeRawTextFingerprint, findConflictRecoveryTarget,
  findRecoveryCopyCandidate, formatRecoverySelectionSnapshot, getHistoryRuntimeId, getPrettyDocumentRecoveryState,
  hasActivePrettyRecoveryForDocument, hasUnpersistedPrettyRecoveryForDocument, persistPrettyDocumentRecoveryState,
  type PrettyCaretAffinityState, type PrettyDocumentRecoveryState, type PrettyPendingConflictIntention,
  type PrettyPendingConflictRecord, type PrettyPendingStaleEdit, type PrettyRecoveryOverflowEntry,
  promoteOverflowRecordIfPossible, recomputeRecoveryRawSegmentRanges, recomputeRecoveryRetainedBytes,
  removeStaleEditRecoveryTarget,
} from "./pretty-replica-recovery.ts";
import { createPrettyRecoveryUiExtension, refreshPrettyRecoveryUi, syncPrettyRecoveryUiAndDiagnostics, type PrettyRecoveryUiController } from "./pretty-replica-recovery-ui.ts";
import { formatPrettyProjection } from "./pretty-segmenter.ts";
import type {
  PrettyCaretAffinity, PrettyDisplayChange, PrettyEditIntentKind, PrettyFormatOptions, PrettyLineCol,
  PrettyMappedSourceChange, PrettyProjectionResult, PrettyRawRange, PrettySelectionRange, PrettySourceMap,
} from "./pretty-types.ts";

export type PrettyLineGutterSourceFacet = Facet<
  (state: EditorState, line: number) => { label: string; kind?: "insert" | "delete"; foldable?: boolean },
  ((state: EditorState, line: number) => { label: string; kind?: "insert" | "delete"; foldable?: boolean }) | undefined
>;
export interface PrettyReplicaAdapterOptions {
  readonly path?: string; readonly languageHint?: string; readonly indentUnit?: string; readonly ringBufferCapacity?: number;
  readonly onAuthoritativeDocChange?: (rawText: string, userEvent: string | undefined) => void;
  readonly lineGutterSourceFacet?: PrettyLineGutterSourceFacet;
}

export type PrettyConflictResolutionAction =
  | { readonly action: "apply"; readonly conflictId?: string; readonly order: number; readonly discardRemainingIntentions?: boolean }
  | { readonly action: "discard-intention"; readonly conflictId?: string; readonly order: number }
  | { readonly action: "discard-conflict"; readonly conflictId?: string };

export type PrettyConflictResolutionResult =
  | {
      readonly ok: true;
      readonly status: "applied" | "discarded-intention" | "discarded-conflict";
      readonly conflictId: string;
      readonly remainingIntentions: number;
      readonly remainingConflicts: number;
      readonly preservationBlocked?: boolean;
      readonly storageError?: DocumentStorageError | null;
    }
  | {
      readonly ok: false;
      readonly status:
        | "no-pending-conflict"
        | "invalid-choice"
        | "incarnation-mismatch"
        | "epoch-mismatch"
        | "overlap-conflict"
        | "chain-unavailable"
        | "admission-refused";
      readonly error?: unknown;
    };

function toEditorSelection(ranges: readonly PrettySelectionRange[], docLength: number): EditorSelection {
  if (ranges.length === 0) return EditorSelection.single(0);
  const clamp = (v: number) => Math.max(0, Math.min(docLength, v));
  return EditorSelection.create(ranges.map((r) => EditorSelection.range(clamp(r.anchor), clamp(r.head))));
}

function computeMinimalTextChange(
  fromText: string,
  toText: string,
): { readonly from: number; readonly to: number; readonly insert: string } | null {
  if (fromText === toText) return null;
  let prefix = 0;
  const minLen = Math.min(fromText.length, toText.length);
  while (prefix < minLen && fromText.charCodeAt(prefix) === toText.charCodeAt(prefix)) prefix++;
  let fromEnd = fromText.length;
  let toEnd = toText.length;
  while (fromEnd > prefix && toEnd > prefix && fromText.charCodeAt(fromEnd - 1) === toText.charCodeAt(toEnd - 1)) {
    fromEnd--;
    toEnd--;
  }
  return { from: prefix, to: fromEnd, insert: toText.slice(prefix, toEnd) };
}

function inferEditIntent(tr: Transaction): PrettyEditIntentKind {
  if (tr.isUserEvent("delete.backward")) return "delete.backward";
  if (tr.isUserEvent("delete.forward")) return "delete.forward";
  if (tr.isUserEvent("input.paste")) return "paste";
  if (tr.isUserEvent("delete.cut")) return "cut";
  if (tr.isUserEvent("input.type")) return "type";
  return "replace";
}

function inferCaretAffinity(map: PrettySourceMap, dispOffset: number): PrettyCaretAffinityState | null {
  for (const run of map.runs) {
    if (run.kind !== "virtual-layout" || run.dispFrom === run.dispTo) continue;
    if (dispOffset === run.dispFrom) return { rawOffset: run.srcFrom, assoc: "before" };
    if (dispOffset === run.dispTo) return { rawOffset: run.srcTo, assoc: "after" };
  }
  return null;
}

function mapRawOffsetWithAffinity(
  map: PrettySourceMap,
  rawOffset: number,
  affinity: PrettyCaretAffinityState | null,
): number {
  const bias: "left" | "right" =
    affinity?.rawOffset === rawOffset ? (affinity.assoc === "before" ? "left" : "right") : "right";
  return sourceToDisplayOffset(map, rawOffset, bias);
}

export class PrettyReplicaEditorAdapter {
  readonly ringBuffer: PrettyRevisionDeltaRingBuffer;
  private view: EditorView | undefined;
  private prettyEnabled = true;
  private holdProjection = false;
  private composing = false;
  private viewCaretAffinity: PrettyCaretAffinityState | null = null;
  private transientRawSegmentRanges: PrettyRawRange[] = [];
  private projectedIncarnation: string;
  private projectedEpoch: number;
  private projectedRevision: number;
  private projectedRawText: string;
  private projection: PrettyProjectionResult;
  private unsubscribeReplica: (() => void) | undefined;
  private lastObservedRevision: number;
  private lastObservedEpoch: number;
  private lastObservedIncarnation: string;
  private lastObservedDocState: EditorState;
  private recoveryUiSeq = 0;
  lastAdmissionError: unknown = null;
  lastCopiedSelection: { readonly displayText: string; readonly rawText: string } | null = null;

  constructor(
    readonly replica: DocumentReplica,
    private readonly options: PrettyReplicaAdapterOptions = {},
  ) {
    this.ringBuffer = new PrettyRevisionDeltaRingBuffer(options.ringBufferCapacity ?? 64);
    this.projectedIncarnation = replica.incarnation;
    this.projectedEpoch = replica.accepted.epoch;
    this.projectedRevision = replica.history.revision;
    this.projectedRawText = replica.currentText;
    this.lastObservedRevision = replica.history.revision;
    this.lastObservedEpoch = replica.accepted.epoch;
    this.lastObservedIncarnation = replica.incarnation;
    this.lastObservedDocState = replica.history.state;
    this.projection = this.computeProjection(this.projectedRawText, this.projectedRevision);
    this.observeReplica();
  }

  private recoveryState(): PrettyDocumentRecoveryState {
    return getPrettyDocumentRecoveryState(this.replica.accepted.id, this.replica.incarnation);
  }

  private uiController(): PrettyRecoveryUiController {
    return {
      getRecoveryState: () => this.recoveryState(),
      getCurrentProjection: () => this.projection,
      copyPendingRecovery: (id) => this.copyPendingRecovery(id),
      copyUnreadableRecoveryPayload: () => this.copyUnreadableRecoveryPayload(),
      setRecoveryFeedback: (msg) => this.setRecoveryFeedback(msg),
      resolvePendingConflict: (res) =>
        this.resolvePendingConflict(res as PrettyConflictResolutionAction),
      discardPendingStaleEdit: (id) => this.discardPendingStaleEdit(id),
      selectRecoveryRecord: (id) => this.selectRecoveryRecord(id),
    };
  }

  private syncRecoveryUi(): void {
    syncPrettyRecoveryUiAndDiagnostics(this.view, this.uiController(), ++this.recoveryUiSeq);
  }

  get rawText(): string { return this.replica.currentText; }
  get currentProjection(): PrettyProjectionResult { return this.projection; }
  get isPrettyEnabled(): boolean { return this.prettyEnabled; }
  get isComposing(): boolean { return this.composing || Boolean(this.view?.composing); }
  get pendingConflicts(): readonly PrettyPendingConflictRecord[] { return this.recoveryState().pendingConflicts; }
  get pendingConflict(): PrettyPendingConflictRecord | null {
    const r = this.recoveryState();
    if (r.pendingConflicts.length > 0) return r.pendingConflicts[0]!;
    if (r.capacityOverflowRecord?.kind === "conflict") return r.capacityOverflowRecord.record;
    return null;
  }
  get pendingStaleEdits(): readonly PrettyPendingStaleEdit[] { return this.recoveryState().pendingStaleEdits; }
  get pendingStaleEdit(): PrettyPendingStaleEdit | null {
    const r = this.recoveryState();
    if (r.pendingStaleEdits.length > 0) return r.pendingStaleEdits[0]!;
    if (r.capacityOverflowRecord?.kind === "stale-edit") return r.capacityOverflowRecord.record;
    return null;
  }
  get caretAffinity(): PrettyCaretAffinityState | null { return this.viewCaretAffinity; }
  get preservationBlocked(): boolean { return this.recoveryState().preservationBlocked || this.replica.preservationBlocked; }
  get recoveryStorageError(): DocumentStorageError | null { return this.recoveryState().lastStorageError; }
  get unreadableRecoveryPayload(): string | null { return this.recoveryState().unreadableRawPayload; }
  get capacityOverflowRecord(): PrettyRecoveryOverflowEntry | null { return this.recoveryState().capacityOverflowRecord; }
  get retainedRecoveryBytes(): number { return this.recoveryState().retainedBytes; }

  hasActiveRecovery(): boolean {
    return hasActivePrettyRecoveryForDocument(this.replica.accepted.id, this.replica.incarnation);
  }

  hasUnpersistedRecovery(): boolean {
    return hasUnpersistedPrettyRecoveryForDocument(this.replica.accepted.id);
  }

  mapSourceLineColToDisplay(line: number, col = 1): PrettyLineCol & { readonly srcOffset: number } {
    return sourceLineColToDisplay(this.projection.sourceMap, this.projectedRawText, line, col, "right");
  }

  mapDisplayOffsetToSourceLineCol(dispOffset: number): PrettyLineCol {
    const bias = this.viewCaretAffinity?.assoc === "before" ? "left" : "right";
    return displayOffsetToSourceLineCol(this.projection.sourceMap, this.projectedRawText, dispOffset, bias);
  }

  getLineGutterLabel(dispLineNumber: number): { label: string; foldable?: boolean } {
    if (!this.prettyEnabled) return { label: String(dispLineNumber) };
    const info = this.projection.sourceMap.displayLines[dispLineNumber - 1];
    if (!info) return { label: String(dispLineNumber) };
    return info.isContinuation ? { label: "", foldable: false } : { label: String(info.sourceLine) };
  }

  getGotoLineTarget(view: EditorView): GotoLineTarget | undefined {
    if (!this.prettyEnabled) return undefined;
    let rawLines = 1;
    for (let i = 0; i < this.projectedRawText.length; i++) {
      if (this.projectedRawText.charCodeAt(i) === 0x0a) rawLines++;
    }
    const currentSrc = this.mapDisplayOffsetToSourceLineCol(view.state.selection.main.head);
    return {
      lineCount: rawLines,
      currentLine: currentSrc.line,
      focus: () => view.focus(),
      goTo: (line, column) => {
        const pos = Math.max(0, Math.min(view.state.doc.length, this.mapSourceLineColToDisplay(line, column).offset));
        view.dispatch({
          selection: { anchor: pos },
          effects: EditorView.scrollIntoView(pos, { y: "center" }),
          userEvent: "select.gotoLine",
        });
      },
    };
  }

  holdViewProjection(hold: boolean): void {
    this.holdProjection = hold;
    if (!hold && !(this.composing || Boolean(this.view?.composing)) && this.projectedRevision !== this.replica.history.revision) {
      this.syncReplicaToView(true);
    }
  }

  private computeProjection(rawText: string, revision: number): PrettyProjectionResult {
    const recovery = this.recoveryState();
    if (!this.prettyEnabled) {
      return formatPrettyProjection(rawText, {
        sourceIdentity: this.replica.accepted.id,
        sourceRevision: revision,
        path: "passthrough.ts",
        languageHint: "typescript",
      });
    }
    const fmtOpts: PrettyFormatOptions = {
      sourceIdentity: this.replica.accepted.id,
      sourceRevision: revision,
      path: this.options.path ?? this.replica.accepted.path,
      languageHint: this.options.languageHint,
      indentUnit: this.options.indentUnit ?? "  ",
      rawSegmentRanges: [...recovery.rawSegmentRanges, ...this.transientRawSegmentRanges],
    };
    return formatPrettyProjection(rawText, fmtOpts);
  }

  private observeReplica(): void {
    const onYText = (event: Y.YTextEvent): void => {
      const nextRev = this.replica.history.revision;
      const nextEpoch = this.replica.accepted.epoch;
      const nextInc = this.replica.incarnation;
      if (nextEpoch !== this.lastObservedEpoch || nextInc !== this.lastObservedIncarnation) {
        this.ringBuffer.invalidateAll(nextRev);
      } else if (nextRev > this.lastObservedRevision) {
        const changes = this.lastObservedDocState.changes(documentDeltaChanges(event.delta));
        this.ringBuffer.recordStep({
          incarnation: nextInc,
          epoch: nextEpoch,
          fromRevision: this.lastObservedRevision,
          toRevision: nextRev,
          changes,
        });
      }
      this.lastObservedRevision = nextRev;
      this.lastObservedEpoch = nextEpoch;
      this.lastObservedIncarnation = nextInc;
      this.lastObservedDocState = this.replica.history.state;
    };
    this.replica.text.observe(onYText);
    const unsub = this.replica.subscribe(() => {
      if (this.replica.accepted.epoch !== this.lastObservedEpoch || this.replica.incarnation !== this.lastObservedIncarnation) {
        this.ringBuffer.invalidateAll(this.replica.history.revision);
        this.lastObservedEpoch = this.replica.accepted.epoch;
        this.lastObservedIncarnation = this.replica.incarnation;
        this.lastObservedRevision = this.replica.history.revision;
        this.lastObservedDocState = this.replica.history.state;
      }
      if (!this.holdProjection && !(this.composing || Boolean(this.view?.composing)) && this.projectedRevision !== this.replica.history.revision) {
        this.syncReplicaToView(true);
      }
    });
    this.unsubscribeReplica = () => { this.replica.text.unobserve(onYText); unsub(); };
  }

  attach(view: EditorView): void {
    this.replica.detachEditor(view);
    this.view = view;
    this.projection = this.computeProjection(this.projectedRawText, this.projectedRevision);
    const rawSel = this.replica.history.state.selection;
    const dispRanges = rawSel.ranges.map((r) => ({
      anchor: mapRawOffsetWithAffinity(this.projection.sourceMap, r.anchor, this.viewCaretAffinity),
      head: mapRawOffsetWithAffinity(this.projection.sourceMap, r.head, this.viewCaretAffinity),
    }));
    const nextSel = toEditorSelection(dispRanges, this.projection.displayText.length);
    const diff = computeMinimalTextChange(view.state.doc.toString(), this.projection.displayText);
    if (diff || !view.state.selection.eq(nextSel, true)) {
      view.dispatch({
        ...(diff ? { changes: diff } : {}),
        selection: nextSel,
        effects: [refreshPrettyRecoveryUi.of(++this.recoveryUiSeq)],
        annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
      });
    }
    this.syncRecoveryUi();
  }

  detach(): void {
    if (this.view) {
      this.replica.detachEditor(this.view);
      this.view = undefined;
    }
    this.composing = false;
    this.replica.history.boundary();
    this.replica.clearPresence();
  }

  destroy(): void {
    this.detach();
    this.unsubscribeReplica?.();
    this.unsubscribeReplica = undefined;
  }

  setPrettyEnabled(enabled: boolean): void {
    if (this.prettyEnabled === enabled) return;
    this.transientRawSegmentRanges = [];
    const currentDispHead = this.view?.state.selection.main.head ?? 0;
    const rawHead = this.prettyEnabled
      ? displayToSourceOffset(
          this.projection.sourceMap,
          currentDispHead,
          this.viewCaretAffinity?.assoc === "before" ? "left" : "right",
        )
      : currentDispHead;
    this.prettyEnabled = enabled;
    this.projectedRawText = this.replica.currentText;
    this.projectedRevision = this.replica.history.revision;
    this.projectedEpoch = this.replica.accepted.epoch;
    this.projectedIncarnation = this.replica.incarnation;
    this.projection = this.computeProjection(this.projectedRawText, this.projectedRevision);
    const nextDispHead = enabled
      ? mapRawOffsetWithAffinity(this.projection.sourceMap, rawHead, this.viewCaretAffinity)
      : Math.min(this.projection.displayText.length, rawHead);
    if (this.view) {
      const diff = computeMinimalTextChange(
        this.view.state.doc.toString(),
        this.projection.displayText,
      );
      this.view.dispatch({
        ...(diff ? { changes: diff } : {}),
        selection: EditorSelection.single(nextDispHead),
        effects: [refreshPrettyRecoveryUi.of(++this.recoveryUiSeq)],
        annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
      });
    }
  }

  stepHistory(direction: "undo" | "redo"): boolean {
    if (this.view?.state.readOnly) return false;
    const prevRev = this.replica.history.revision;
    this.replica.stepHistory(direction);
    if (this.replica.history.revision !== prevRev) {
      this.transientRawSegmentRanges = [];
      this.syncReplicaToView(false, direction);
      this.options.onAuthoritativeDocChange?.(this.replica.currentText, direction);
    }
    return true;
  }

  private finalizeRecoveryMutation(recovery: PrettyDocumentRecoveryState) {
    recomputeRecoveryRetainedBytes(recovery);
    promoteOverflowRecordIfPossible(recovery);
    recomputeRecoveryRawSegmentRanges(recovery);
    if (
      recovery.pendingConflicts.length === 0 &&
      recovery.pendingStaleEdits.length === 0 &&
      recovery.capacityOverflowRecord === null
    ) {
      recovery.lastRecoveryFeedback = null;
      recovery.selectedRecoveryRecordId = null;
    }
    const persisted = persistPrettyDocumentRecoveryState(recovery);
    this.syncReplicaToView(false);
    this.syncRecoveryUi();
    return persisted;
  }

  private failConflictResolution(
    recovery: PrettyDocumentRecoveryState,
    status: Extract<PrettyConflictResolutionResult, { readonly ok: false }>["status"],
    error?: unknown,
  ): PrettyConflictResolutionResult {
    recovery.lastRecoveryFeedback = `Cannot insert retained edit: ${status}`;
    this.syncRecoveryUi();
    return error !== undefined ? { ok: false, status, error } : { ok: false, status };
  }

  resolvePendingConflict(resolution: PrettyConflictResolutionAction): PrettyConflictResolutionResult {
    const recovery = this.recoveryState();
    const target = findConflictRecoveryTarget(recovery, resolution.conflictId);
    if (!target) return { ok: false, status: "no-pending-conflict" };
    const { conflict, remove } = target;

    if (resolution.action === "discard-conflict") {
      remove();
      recovery.lastRecoveryFeedback = "Dismissed retained edit";
      const persisted = this.finalizeRecoveryMutation(recovery);
      return {
        ok: true,
        status: "discarded-conflict",
        conflictId: conflict.conflictId,
        remainingIntentions: 0,
        remainingConflicts: recovery.pendingConflicts.length,
        preservationBlocked: recovery.preservationBlocked,
        storageError: persisted.ok ? null : persisted.error,
      };
    }

    if (resolution.action === "discard-intention") {
      const intIdx = conflict.intentions.findIndex((item) => item.order === resolution.order);
      if (intIdx < 0) return this.failConflictResolution(recovery, "invalid-choice");
      conflict.intentions.splice(intIdx, 1);
      if (conflict.intentions.length === 0) remove();
      recovery.lastRecoveryFeedback = `Dismissed retained edit #${resolution.order}`;
      const persisted = this.finalizeRecoveryMutation(recovery);
      return {
        ok: true,
        status: "discarded-intention",
        conflictId: conflict.conflictId,
        remainingIntentions: conflict.intentions.length,
        remainingConflicts: recovery.pendingConflicts.length,
        preservationBlocked: recovery.preservationBlocked,
        storageError: persisted.ok ? null : persisted.error,
      };
    }

    const chosen = conflict.intentions.find((item) => item.order === resolution.order);
    if (!chosen) return this.failConflictResolution(recovery, "invalid-choice");
    if (conflict.documentId !== this.replica.accepted.id || chosen.baseIncarnation !== this.replica.incarnation) {
      return this.failConflictResolution(recovery, "incarnation-mismatch");
    }
    if (chosen.baseEpoch !== this.replica.accepted.epoch) {
      return this.failConflictResolution(recovery, "epoch-mismatch");
    }

    const currentRuntimeId = getHistoryRuntimeId(this.replica.history);
    const currentRawText = this.replica.currentText;
    const currentRawFingerprint = computeRawTextFingerprint(currentRawText);
    let targetChange: PrettyMappedSourceChange;

    if (chosen.baseRuntimeId !== currentRuntimeId || chosen.baseHistoryRevision === this.replica.history.revision) {
      if (chosen.baseRawFingerprint !== currentRawFingerprint) {
        return this.failConflictResolution(recovery, "chain-unavailable");
      }
      targetChange = { from: chosen.rawFrom, to: chosen.rawTo, insert: chosen.insert };
    } else {
      const rebased = rebaseChangesOverDeltaChain(
        this.ringBuffer, chosen.baseIncarnation, chosen.baseEpoch, chosen.baseHistoryRevision,
        this.replica.incarnation, this.replica.accepted.epoch, this.replica.history.revision,
        currentRawText.length, [{ from: chosen.rawFrom, to: chosen.rawTo, insert: chosen.insert }], chosen.rawFrom,
      );
      if (rebased.status !== "disjoint") return this.failConflictResolution(recovery, rebased.status);
      targetChange = rebased.changes[0]!;
    }

    const prevRev = this.replica.history.revision;
    const rawTx = this.replica.history.state.update({
      changes: { from: targetChange.from, to: targetChange.to, insert: targetChange.insert },
      selection: EditorSelection.single(targetChange.from + targetChange.insert.length),
      userEvent: "input.type",
    });

    try {
      this.replica.acceptEditorTransaction(rawTx);
    } catch (error) {
      this.lastAdmissionError = error;
      return this.failConflictResolution(recovery, "admission-refused", error);
    }

    const postCommitFingerprint = computeRawTextFingerprint(this.replica.currentText);
    if (resolution.discardRemainingIntentions === true) {
      remove();
    } else {
      const remaining: PrettyPendingConflictIntention[] = [];
      for (const sib of conflict.intentions) {
        if (sib.order === chosen.order) continue;
        let sibAtPrev: PrettyMappedSourceChange | null = null;
        if (sib.baseRuntimeId !== currentRuntimeId) {
          if (sib.baseRawFingerprint === currentRawFingerprint) {
            sibAtPrev = { from: sib.rawFrom, to: sib.rawTo, insert: sib.insert };
          }
        } else {
          const sibRebased = rebaseChangesOverDeltaChain(
            this.ringBuffer, sib.baseIncarnation, sib.baseEpoch, sib.baseHistoryRevision,
            this.replica.incarnation, this.replica.accepted.epoch, prevRev,
            rawTx.startState.doc.length, [{ from: sib.rawFrom, to: sib.rawTo, insert: sib.insert }], sib.rawFrom,
          );
          if (sibRebased.status === "disjoint") sibAtPrev = sibRebased.changes[0]!;
        }
        remaining.push(
          sibAtPrev
            ? {
                ...sib,
                baseRuntimeId: currentRuntimeId,
                baseIncarnation: this.replica.incarnation,
                baseEpoch: this.replica.accepted.epoch,
                baseHistoryRevision: this.replica.history.revision,
                baseRawFingerprint: postCommitFingerprint,
                rawFrom: rawTx.changes.mapPos(sibAtPrev.from, 1),
                rawTo: rawTx.changes.mapPos(sibAtPrev.to, 1),
              }
            : sib,
        );
      }
      conflict.intentions.splice(0, conflict.intentions.length, ...remaining);
      if (conflict.intentions.length === 0) remove();
    }

    recovery.lastRecoveryFeedback = `Inserted retained edit #${chosen.order}`;
    const persisted = this.finalizeRecoveryMutation(recovery);
    this.options.onAuthoritativeDocChange?.(this.replica.currentText, "input.type");
    return {
      ok: true,
      status: "applied",
      conflictId: conflict.conflictId,
      remainingIntentions: resolution.discardRemainingIntentions ? 0 : conflict.intentions.length,
      remainingConflicts: recovery.pendingConflicts.length,
      preservationBlocked: recovery.preservationBlocked,
      storageError: persisted.ok ? null : persisted.error,
    };
  }

  discardPendingStaleEdit(staleEditId?: string): boolean {
    const recovery = this.recoveryState();
    if (!removeStaleEditRecoveryTarget(recovery, staleEditId)) return false;
    recovery.lastRecoveryFeedback = "Dismissed retained edit";
    this.finalizeRecoveryMutation(recovery);
    return true;
  }

  setRecoveryFeedback(message: string): void {
    const recovery = this.recoveryState();
    if (recovery.lastRecoveryFeedback === message) return;
    recovery.lastRecoveryFeedback = message;
    this.syncRecoveryUi();
  }

  copyPendingRecovery(targetId?: string): { readonly displayText: string; readonly rawText: string } | null {
    const recovery = this.recoveryState();
    const chosen = findRecoveryCopyCandidate(recovery, targetId);
    if (!chosen) return null;
    const snap = formatRecoverySelectionSnapshot(chosen);
    this.lastCopiedSelection = snap;
    recovery.lastRecoveryFeedback =
      "Selected retained edit in text box — press Copy shortcut to copy";
    this.syncRecoveryUi();
    return snap;
  }

  copyUnreadableRecoveryPayload(): string | null {
    const recovery = this.recoveryState();
    if (!recovery.unreadableRawPayload) return null;
    this.lastCopiedSelection = {
      displayText: recovery.unreadableRawPayload,
      rawText: recovery.unreadableRawPayload,
    };
    recovery.lastRecoveryFeedback =
      "Selected retained edit in text box — press Copy shortcut to copy";
    this.syncRecoveryUi();
    return recovery.unreadableRawPayload;
  }

  selectRecoveryRecord(recordId: string): void {
    const recovery = this.recoveryState();
    recovery.selectedRecoveryRecordId = recordId;
    this.syncRecoveryUi();
  }

  startComposition(): void {
    this.composing = true;
    this.holdProjection = true;
  }

  finishComposition(): void {
    if (!this.composing && !this.view?.composing) return;
    this.composing = false;
    this.holdProjection = false;
    this.transientRawSegmentRanges = [];
    const probe = this.computeProjection(this.replica.currentText, this.replica.history.revision);
    if (probe.outcome !== "formatted") {
      this.transientRawSegmentRanges = [{ srcFrom: 0, srcTo: this.replica.currentText.length }];
    }
    this.syncReplicaToView(false);
  }

  copySelection(): { readonly displayText: string; readonly rawText: string } {
    if (!this.view) return { displayText: "", rawText: "" };
    const ranges = this.view.state.selection.ranges.map((r) => ({ anchor: r.anchor, head: r.head }));
    const res = extractCopySelection(
      this.projection.sourceMap,
      this.projectedRawText,
      this.projection.displayText,
      ranges,
    );
    this.lastCopiedSelection = res;
    return res;
  }

  private syncReplicaToView(remote: boolean, userEvent?: string): void {
    const currentRawText = this.replica.currentText;
    if (remote && this.viewCaretAffinity) {
      const chain = this.ringBuffer.collectChain(
        this.projectedIncarnation,
        this.projectedEpoch,
        this.projectedRevision,
        this.replica.incarnation,
        this.replica.accepted.epoch,
        this.replica.history.revision,
      );
      if (chain && chain.length > 0) {
        let mappedRaw = this.viewCaretAffinity.rawOffset;
        const assocSide = this.viewCaretAffinity.assoc === "before" ? -1 : 1;
        for (const step of chain) mappedRaw = step.changes.mapPos(mappedRaw, assocSide);
        this.viewCaretAffinity = { rawOffset: mappedRaw, assoc: this.viewCaretAffinity.assoc };
      }
    }
    this.projectedRawText = currentRawText;
    this.projectedRevision = this.replica.history.revision;
    this.projectedEpoch = this.replica.accepted.epoch;
    this.projectedIncarnation = this.replica.incarnation;
    this.projection = this.computeProjection(this.projectedRawText, this.projectedRevision);

    if (!this.view) return;
    const rawSel = this.replica.history.state.selection;
    const dispRanges = rawSel.ranges.map((r) => ({
      anchor: mapRawOffsetWithAffinity(this.projection.sourceMap, r.anchor, this.viewCaretAffinity),
      head: mapRawOffsetWithAffinity(this.projection.sourceMap, r.head, this.viewCaretAffinity),
    }));
    const nextSel = toEditorSelection(dispRanges, this.projection.displayText.length);
    const diff = computeMinimalTextChange(
      this.view.state.doc.toString(),
      this.projection.displayText,
    );
    if (!diff && this.view.state.selection.eq(nextSel, true)) return;
    this.view.dispatch({
      ...(diff ? { changes: diff } : {}),
      selection: nextSel,
      ...(userEvent ? { userEvent } : {}),
      annotations: [
        documentSynchronization.of(true),
        documentRemoteChange.of(remote),
        Transaction.addToHistory.of(false),
      ],
    });
  }

  private handleLocalTransaction(tr: Transaction): TransactionSpec | readonly TransactionSpec[] {
    if (!tr.docChanged) {
      if (tr.selection) {
        const inferred = inferCaretAffinity(this.projection.sourceMap, tr.selection.main.head);
        this.viewCaretAffinity = inferred;
        const side = inferred?.assoc === "before" ? "left" : "right";
        const rawRanges = tr.selection.ranges.map((r) => ({
          anchor: displayToSourceOffset(this.projection.sourceMap, r.anchor, side),
          head: displayToSourceOffset(this.projection.sourceMap, r.head, side),
        }));
        const rawSel = toEditorSelection(rawRanges, this.replica.history.state.doc.length);
        this.replica.history.apply({ selection: rawSel, annotations: Transaction.addToHistory.of(false) });
        if (this.view?.hasFocus) this.replica.publishPresence(rawSel);
      }
      return tr;
    }

    const displayChanges: PrettyDisplayChange[] = [];
    tr.changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
      displayChanges.push({ from: fromA, to: toA, insert: inserted.toString() });
    });
    const intent = inferEditIntent(tr);
    const isComposeEvent = this.composing || Boolean(this.view?.composing) || tr.isUserEvent("input.type.compose");
    if (isComposeEvent && !this.composing) this.startComposition();

    const mapped = mapDisplayEditToSource({
      map: this.projection.sourceMap,
      rawText: this.projectedRawText,
      displayText: this.projection.displayText,
      changes: displayChanges,
      selection: tr.startState.selection.ranges.map((r) => ({ anchor: r.anchor, head: r.head })),
      intent,
      composing: isComposeEvent,
      segments: this.projection.segments,
    });

    if (
      intent === "cut" &&
      (mapped.copiedDisplayText !== undefined || mapped.copiedRawText !== undefined) &&
      ((this.pendingConflict === null && this.pendingStaleEdit === null) || this.lastCopiedSelection === null)
    ) {
      this.lastCopiedSelection = {
        displayText: mapped.copiedDisplayText ?? "",
        rawText: mapped.copiedRawText ?? "",
      };
    }

    if (mapped.movedToRawBoundary) {
      const rawHead = mapped.rawSelection[0]?.head ?? 0;
      const assoc: PrettyCaretAffinity = mapped.caretAffinity ?? "after";
      this.viewCaretAffinity = { rawOffset: rawHead, assoc };
      const rawSel = toEditorSelection(mapped.rawSelection, this.replica.history.state.doc.length);
      this.replica.history.apply({ selection: rawSel, annotations: Transaction.addToHistory.of(false) });
      if (this.view?.hasFocus) this.replica.publishPresence(rawSel);
      return {
        selection: toEditorSelection(mapped.displaySelection, tr.startState.doc.length),
        annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
      };
    }

    const baseRuntimeId = getHistoryRuntimeId(this.replica.history);
    const baseRawFingerprint = computeRawTextFingerprint(this.projectedRawText);

    if (mapped.conflictRequiresRawFallback && mapped.conflict) {
      const appendRes = appendMultiCursorConflictToRecovery(
        this.recoveryState(), baseRuntimeId, this.projectedIncarnation, this.projectedEpoch,
        this.projectedRevision, baseRawFingerprint, mapped.conflict, mapped.affectedRawRanges,
      );
      if (appendRes.status === "refused-at-capacity") {
        this.lastAdmissionError = appendRes.error;
      } else if (this.lastCopiedSelection === null) {
        this.lastCopiedSelection = formatRecoverySelectionSnapshot(appendRes.record);
      }
      this.projectedRawText = this.replica.currentText;
      this.projectedRevision = this.replica.history.revision;
      this.projection = this.computeProjection(this.projectedRawText, this.projectedRevision);
      const rawSel = toEditorSelection(mapped.rawSelection, this.replica.history.state.doc.length);
      this.replica.history.apply({ selection: rawSel, annotations: Transaction.addToHistory.of(false) });
      const dispRanges = mapped.rawSelection.map((r) => ({
        anchor: sourceToDisplayOffset(this.projection.sourceMap, r.anchor, "right"),
        head: sourceToDisplayOffset(this.projection.sourceMap, r.head, "right"),
      }));
      const diff = computeMinimalTextChange(tr.startState.doc.toString(), this.projection.displayText);
      queueMicrotask(() => this.syncRecoveryUi());
      return {
        ...(diff ? { changes: diff } : {}),
        selection: toEditorSelection(dispRanges, this.projection.displayText.length),
        effects: [refreshPrettyRecoveryUi.of(++this.recoveryUiSeq)],
        annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
      };
    }

    if (mapped.changes.length === 0) return [];

    if (
      this.projectedRevision !== this.replica.history.revision ||
      this.projectedEpoch !== this.replica.accepted.epoch ||
      this.projectedIncarnation !== this.replica.incarnation
    ) {
      const rebased = rebaseChangesOverDeltaChain(
        this.ringBuffer, this.projectedIncarnation, this.projectedEpoch, this.projectedRevision,
        this.replica.incarnation, this.replica.accepted.epoch, this.replica.history.revision,
        this.replica.currentText.length, mapped.changes, mapped.changes[0]?.from ?? 0,
      );
      if (rebased.status !== "disjoint") {
        const appendRes = appendStaleEditToRecovery(
          this.recoveryState(), baseRuntimeId, this.projectedIncarnation, this.projectedEpoch,
          this.projectedRevision, baseRawFingerprint, this.replica.incarnation,
          this.replica.accepted.epoch, this.replica.history.revision,
          rebased.status, displayChanges, mapped.changes, rebased.rebasedCursorOffset,
        );
        if (appendRes.status === "refused-at-capacity") {
          this.lastAdmissionError = appendRes.error;
        } else if (this.lastCopiedSelection === null) {
          this.lastCopiedSelection = formatRecoverySelectionSnapshot(appendRes.record);
        }
        this.holdProjection = false;
        this.projectedRawText = this.replica.currentText;
        this.projectedRevision = this.replica.history.revision;
        this.projectedEpoch = this.replica.accepted.epoch;
        this.projectedIncarnation = this.replica.incarnation;
        this.projection = this.computeProjection(this.projectedRawText, this.projectedRevision);
        const dispCursor = sourceToDisplayOffset(this.projection.sourceMap, rebased.rebasedCursorOffset, "right");
        const diff = computeMinimalTextChange(tr.startState.doc.toString(), this.projection.displayText);
        queueMicrotask(() => this.syncRecoveryUi());
        return {
          ...(diff ? { changes: diff } : {}),
          selection: EditorSelection.single(dispCursor),
          effects: [refreshPrettyRecoveryUi.of(++this.recoveryUiSeq)],
          annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
        };
      }
      return this.commitRawChanges(
        tr, displayChanges, rebased.changes,
        [{ anchor: rebased.rebasedCursorOffset, head: rebased.rebasedCursorOffset }],
        mapped.requiresRawFallback, isComposeEvent,
      );
    }

    return this.commitRawChanges(
      tr, displayChanges, mapped.changes, mapped.rawSelection, mapped.requiresRawFallback, isComposeEvent,
    );
  }

  private commitRawChanges(
    tr: Transaction,
    displayChanges: readonly PrettyDisplayChange[],
    rawChanges: readonly PrettyMappedSourceChange[],
    rawSelectionRanges: readonly PrettySelectionRange[],
    requiresRawFallback: boolean,
    isComposeEvent: boolean,
  ): TransactionSpec | readonly TransactionSpec[] {
    const timestamp = tr.annotation(Transaction.time);
    const isolation = tr.annotation(isolateHistory);
    const userEvent = tr.annotation(Transaction.userEvent) ?? "input.type";
    const changesSpec = rawChanges.map((c) => ({ from: c.from, to: c.to, insert: c.insert }));
    const txAnnotations = [
      ...(timestamp !== undefined ? [Transaction.time.of(timestamp)] : []),
      Transaction.addToHistory.of(tr.annotation(Transaction.addToHistory) !== false),
      ...(isolation !== undefined ? [isolateHistory.of(isolation)] : []),
    ];

    const rawTx = this.replica.history.state.update({ changes: changesSpec, annotations: txAnnotations, userEvent });
    const rawSel = toEditorSelection(rawSelectionRanges, rawTx.newDoc.length);
    const finalRawTx = this.replica.history.state.update({
      changes: changesSpec,
      selection: rawSel,
      annotations: txAnnotations,
      userEvent,
    });

    try {
      this.replica.acceptEditorTransaction(finalRawTx);
    } catch (error) {
      this.lastAdmissionError = error;
      return [];
    }

    this.viewCaretAffinity = null;

    if (isComposeEvent) {
      this.projectedRawText = this.replica.currentText;
      this.projectedRevision = this.replica.history.revision;
      this.projectedEpoch = this.replica.accepted.epoch;
      this.projectedIncarnation = this.replica.incarnation;
      this.projection = advanceProjectionForComposeFrame(
        this.projection, displayChanges, rawChanges, this.projectedRawText, tr.newDoc.toString(), this.projectedRevision,
      );
      return tr;
    }

    this.transientRawSegmentRanges = requiresRawFallback
      ? rawChanges.map((c) => ({ srcFrom: c.from, srcTo: c.from + c.insert.length }))
      : [];

    this.holdProjection = false;
    this.projectedRawText = this.replica.currentText;
    this.projectedRevision = this.replica.history.revision;
    this.projectedEpoch = this.replica.accepted.epoch;
    this.projectedIncarnation = this.replica.incarnation;
    this.projection = this.computeProjection(this.projectedRawText, this.projectedRevision);

    const dispRanges = rawSelectionRanges.map((r) => ({
      anchor: sourceToDisplayOffset(this.projection.sourceMap, r.anchor, "right"),
      head: sourceToDisplayOffset(this.projection.sourceMap, r.head, "right"),
    }));
    if (this.view?.hasFocus) this.replica.publishPresence(rawSel);
    const updatedRaw = this.replica.currentText;
    queueMicrotask(() => this.options.onAuthoritativeDocChange?.(updatedRaw, userEvent));
    const diff = computeMinimalTextChange(tr.startState.doc.toString(), this.projection.displayText);
    return {
      ...(diff ? { changes: diff } : {}),
      selection: toEditorSelection(dispRanges, this.projection.displayText.length),
      userEvent,
      annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
    };
  }

  get extension(): Extension {
    const command = (direction: "undo" | "redo") => () => this.stepHistory(direction);
    const gutterFacet = this.options.lineGutterSourceFacet;
    const gutterStampField = gutterFacet
      ? StateField.define<number>({
          create: () => 0,
          update: (val, tr) => (tr.docChanged || tr.effects.some((e) => e.is(refreshPrettyRecoveryUi)) ? val + 1 : val),
          provide: (field) => gutterFacet.compute([field], () => (_state, dispLine) => this.getLineGutterLabel(dispLine)),
        })
      : [];
    return [
      gutterStampField,
      gotoLineTargetFacet.of((view) => this.getGotoLineTarget(view)),
      createPrettyRecoveryUiExtension(this.uiController()),
      editorHistory.of((direction) => { this.stepHistory(direction); }),
      EditorState.transactionFilter.of((tr) =>
        tr.annotation(documentSynchronization) ? tr : this.handleLocalTransaction(tr),
      ),
      Prec.highest(
        EditorView.domEventHandlers({
          keydown: (event, view) => {
            if (isImeComposition(event)) { this.startComposition(); return false; }
            const direction = view.composing ? null : documentHistoryKey(event);
            if (!direction) return false;
            event.preventDefault();
            return command(direction)();
          },
          copy: () => { this.copySelection(); return false; },
          cut: () => { this.copySelection(); return false; },
          compositionstart: () => { this.startComposition(); return false; },
          compositionupdate: () => { this.startComposition(); return false; },
          compositionend: () => { this.finishComposition(); return false; },
          beforeinput: (event) => {
            if (isImeComposition(event) || event.inputType === "insertCompositionText") {
              this.startComposition();
              return false;
            }
            if (event.inputType === "historyUndo") return command("undo")();
            if (event.inputType === "historyRedo") return command("redo")();
            return false;
          },
          blur: () => { this.replica.history.boundary(); return false; },
        }),
      ),
      Prec.highest(
        keymap.of([{ key: "Mod-y", run: () => shortcutPlatform() !== "macos" && command("redo")() }]),
      ),
    ];
  }
}
