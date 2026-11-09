import {
  Compartment,
  EditorSelection,
  EditorState,
  Facet,
  StateField,
  Transaction,
  type Extension,
} from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import type { SecretScreen } from "../../api/types.ts";
import { lineGutterSource } from "../../components/source/annotations/line-gutter.ts";
import { diffSourceText } from "../../components/source/diff/source-text-diff.ts";
import { clearPrettyProjectionCacheForIdentity } from "../../components/source/pretty/pretty-cache.ts";
import { mapDisplayEditToSource } from "../../components/source/pretty/pretty-edit-mapper.ts";
import {
  createRuneOffsetTable,
  displayOffsetToSourceLineCol,
  displayToSourceOffset,
  mapRuneSpansToDisplay,
  sourceLineColToDisplay,
  sourceToDisplayOffset,
} from "../../components/source/pretty/pretty-map.ts";
import { PrettyReplicaEditorAdapter } from "../../components/source/pretty/pretty-replica-adapter.ts";
import { hasActivePrettyRecoveryForDocument } from "../../components/source/pretty/pretty-replica-recovery.ts";
import { refreshPrettyRecoveryUi } from "../../components/source/pretty/pretty-replica-recovery-ui.ts";
import { formatPrettyProjection } from "../../components/source/pretty/pretty-segmenter.ts";
import type {
  PrettyDisplayChange,
  PrettyLineCol,
  PrettyProjectionResult,
} from "../../components/source/pretty/pretty-types.ts";
import { gotoLineTargetFacet, type GotoLineTarget } from "../../find/goto-line-controller.ts";
import { documentAgentPresence } from "../documents/document-agent-presence.ts";
import { documentChangeHighlights } from "../documents/document-change-highlights.ts";
import { documentSynchronization } from "../documents/document-editor-binding.ts";
import { documentPresence } from "../documents/document-presence.ts";
import type { DocumentReplica } from "../documents/document-replica.ts";

const collaboration = new Compartment();
const boundDocument = Facet.define<DocumentReplica["doc"], DocumentReplica["doc"] | undefined>({
  combine: (values) => values[0],
});
const liveSelection = StateField.define<boolean>({
  create: () => false,
  update: (held, transaction) => held || transaction.selection != null || transaction.docChanged,
});

export interface FilesCollaborationPrettyOptions {
  readonly projectId?: string;
  readonly key?: string;
  readonly path?: string;
  readonly enabled?: boolean;
  readonly onAuthoritativeDocChange?: (rawText: string, userEvent: string | undefined) => void;
}

type FilesViewPrettySession = {
  projectId: string;
  key: string;
  path: string;
  prettyEnabled: boolean;
  replica: DocumentReplica | undefined;
  adapter: PrettyReplicaEditorAdapter | undefined;
  adapterAttached: boolean;
  nonReplicaRawText: string;
  nonReplicaRevision: number;
  nonReplicaProjection: PrettyProjectionResult | undefined;
  onAuthoritativeDocChange: ((rawText: string, userEvent: string | undefined) => void) | undefined;
};

const sessionsByView = new WeakMap<EditorView, FilesViewPrettySession>();

function ensureSession(
  view: EditorView,
  replica: DocumentReplica | undefined,
  options?: FilesCollaborationPrettyOptions,
): FilesViewPrettySession {
  let session = sessionsByView.get(view);
  if (!session) {
    session = {
      projectId: options?.projectId ?? "",
      key: options?.key ?? "",
      path: options?.path ?? replica?.accepted.path ?? "",
      prettyEnabled: options?.enabled === true,
      replica,
      adapter: undefined,
      adapterAttached: false,
      nonReplicaRawText: view.state.doc.toString(),
      nonReplicaRevision: 0,
      nonReplicaProjection: undefined,
      onAuthoritativeDocChange: options?.onAuthoritativeDocChange,
    };
    sessionsByView.set(view, session);
  } else {
    if (options?.projectId !== undefined) session.projectId = options.projectId;
    if (options?.key !== undefined) session.key = options.key;
    if (options?.path !== undefined) session.path = options.path;
    if (options?.enabled !== undefined) session.prettyEnabled = options.enabled;
    if (options?.onAuthoritativeDocChange !== undefined) {
      session.onAuthoritativeDocChange = options.onAuthoritativeDocChange;
    }
    session.replica = replica;
  }
  return session;
}

function buildNonReplicaPrettyExtensions(session: FilesViewPrettySession): Extension {
  const gutterStampField = StateField.define<number>({
    create: () => 0,
    update: (val, tr) => {
      if (tr.docChanged) return val + 1;
      for (const e of tr.effects) {
        if (e.is(refreshPrettyRecoveryUi)) return val + 1;
      }
      return val;
    },
    provide: (field) =>
      lineGutterSource.compute([field], () => (_state, dispLineNumber) => {
        if (!session.prettyEnabled || !session.nonReplicaProjection) {
          return { label: String(dispLineNumber) };
        }
        const info = session.nonReplicaProjection.sourceMap.displayLines[dispLineNumber - 1];
        if (!info) return { label: String(dispLineNumber) };
        if (info.isContinuation) return { label: "", foldable: false };
        return { label: String(info.sourceLine) };
      }),
  });

  const gotoExt = gotoLineTargetFacet.of((view): GotoLineTarget | undefined => {
    if (!session.prettyEnabled || !session.nonReplicaProjection) return undefined;
    let rawLines = 1;
    for (let i = 0; i < session.nonReplicaRawText.length; i++) {
      if (session.nonReplicaRawText.charCodeAt(i) === 0x0a) rawLines++;
    }
    const currentSrc = displayOffsetToSourceLineCol(
      session.nonReplicaProjection.sourceMap,
      session.nonReplicaRawText,
      view.state.selection.main.head,
      "right",
    );
    return {
      lineCount: rawLines,
      currentLine: currentSrc.line,
      focus: () => view.focus(),
      goTo: (line, column) => {
        if (!session.nonReplicaProjection) return;
        const mapped = sourceLineColToDisplay(
          session.nonReplicaProjection.sourceMap,
          session.nonReplicaRawText,
          line,
          column,
          "right",
        );
        const pos = Math.max(0, Math.min(view.state.doc.length, mapped.offset));
        view.dispatch({
          selection: { anchor: pos },
          effects: EditorView.scrollIntoView(pos, { y: "center" }),
          userEvent: "select.gotoLine",
        });
      },
    };
  });

  const txFilter = EditorState.transactionFilter.of((tr) => {
    if (
      tr.annotation(documentSynchronization) ||
      !tr.docChanged ||
      !session.prettyEnabled ||
      !session.nonReplicaProjection
    ) {
      return tr;
    }
    const displayChanges: PrettyDisplayChange[] = [];
    tr.changes.iterChanges((fromA, toA, _fromB, _toB, inserted) => {
      displayChanges.push({ from: fromA, to: toA, insert: inserted.toString() });
    });
    const mapped = mapDisplayEditToSource({
      map: session.nonReplicaProjection.sourceMap,
      rawText: session.nonReplicaRawText,
      displayText: session.nonReplicaProjection.displayText,
      changes: displayChanges,
      selection: tr.startState.selection.ranges.map((r) => ({ anchor: r.anchor, head: r.head })),
      intent: "type",
      composing: false,
      segments: session.nonReplicaProjection.segments,
    });
    if (mapped.movedToRawBoundary) {
      const dispHead = mapped.displaySelection[0]?.head ?? 0;
      return {
        selection: EditorSelection.single(Math.max(0, Math.min(tr.startState.doc.length, dispHead))),
        annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
      };
    }
    if (mapped.changes.length === 0) return [];
    let nextRaw = session.nonReplicaRawText;
    for (let i = mapped.changes.length - 1; i >= 0; i--) {
      const c = mapped.changes[i]!;
      nextRaw = nextRaw.slice(0, c.from) + c.insert + nextRaw.slice(c.to);
    }
    session.nonReplicaRawText = nextRaw;
    session.nonReplicaProjection = formatPrettyProjection(nextRaw, {
      sourceIdentity: session.key || session.path || "non-replica",
      sourceRevision: ++session.nonReplicaRevision,
      path: session.path,
      rawSegmentRanges: mapped.requiresRawFallback
        ? mapped.changes.map((c) => ({ srcFrom: c.from, srcTo: c.from + c.insert.length }))
        : undefined,
    });
    const userEvent = tr.annotation(Transaction.userEvent) ?? "input.type";
    session.onAuthoritativeDocChange?.(session.nonReplicaRawText, userEvent);
    const rawHead = mapped.rawSelection[0]?.head ?? 0;
    const dispHead = sourceToDisplayOffset(
      session.nonReplicaProjection.sourceMap,
      rawHead,
      "right",
    );
    return {
      changes: {
        from: 0,
        to: tr.startState.doc.length,
        insert: session.nonReplicaProjection.displayText,
      },
      selection: EditorSelection.single(
        Math.max(0, Math.min(session.nonReplicaProjection.displayText.length, dispHead)),
      ),
      userEvent,
      annotations: [documentSynchronization.of(true)],
    };
  });

  return [gutterStampField, gotoExt, txFilter];
}

export function filesCollaborationExtension(replica: DocumentReplica | undefined): Extension {
  return [
    liveSelection,
    collaboration.of(replica ? [boundDocument.of(replica.doc), replica.extension] : []),
  ];
}

export function canRestoreFilesCollaborationView(view: EditorView): boolean {
  return !view.state.field(liveSelection);
}

export function syncFilesCollaborationPrettyMode(
  view: EditorView,
  replica: DocumentReplica | undefined,
  options: FilesCollaborationPrettyOptions,
): void {
  const session = ensureSession(view, replica, options);
  const wantPretty = session.prettyEnabled;

  if (replica) {
    session.nonReplicaProjection = undefined;
    if (session.adapter && session.adapter.replica !== replica) {
      session.adapter.destroy();
      session.adapter = undefined;
      session.adapterAttached = false;
    }
    if (!session.adapter) {
      session.adapter = new PrettyReplicaEditorAdapter(replica, {
        path: session.path || replica.accepted.path,
        onAuthoritativeDocChange: (rawText, userEvent) => {
          session.onAuthoritativeDocChange?.(rawText, userEvent);
        },
      });
      session.adapter.setPrettyEnabled(wantPretty);
    }

    const useAdapter =
      wantPretty ||
      session.adapter.hasActiveRecovery() ||
      hasActivePrettyRecoveryForDocument(replica.accepted.id, replica.incarnation);

    if (useAdapter) {
      session.adapter.setPrettyEnabled(wantPretty);
      if (!session.adapterAttached) {
        view.dispatch({
          effects: collaboration.reconfigure([
            boundDocument.of(replica.doc),
            documentChangeHighlights,
            documentPresence(replica),
            documentAgentPresence(replica),
            session.adapter.extension,
          ]),
        });
        session.adapterAttached = true;
      }
      session.adapter.attach(view);
      return;
    }

    if (session.adapterAttached) {
      session.adapter.setPrettyEnabled(false);
      const rawSel = view.state.selection;
      session.adapter.detach();
      session.adapterAttached = false;
      view.dispatch({
        effects: collaboration.reconfigure([boundDocument.of(replica.doc), replica.extension]),
      });
      if (!view.state.selection.eq(rawSel, true)) {
        view.dispatch({
          selection: rawSel,
          annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
        });
      }
    }
    return;
  }

  // Non-replica buffer path (e.g., before replica attaches or read-only/lightweight views)
  if (session.adapter) {
    session.adapter.destroy();
    session.adapter = undefined;
    session.adapterAttached = false;
  }

  if (wantPretty) {
    if (!session.nonReplicaProjection) {
      session.nonReplicaRawText = view.state.doc.toString();
    }
    const rawHead = session.nonReplicaProjection
      ? displayToSourceOffset(
          session.nonReplicaProjection.sourceMap,
          view.state.selection.main.head,
          "right",
        )
      : view.state.selection.main.head;
    const proj = formatPrettyProjection(session.nonReplicaRawText, {
      sourceIdentity: session.key || session.path || "non-replica",
      sourceRevision: ++session.nonReplicaRevision,
      path: session.path,
    });
    session.nonReplicaProjection = proj;
    const dispHead = sourceToDisplayOffset(proj.sourceMap, rawHead, "right");
    view.dispatch({
      effects: collaboration.reconfigure(buildNonReplicaPrettyExtensions(session)),
    });
    const currentDoc = view.state.doc.toString();
    if (currentDoc !== proj.displayText || view.state.selection.main.head !== dispHead) {
      view.dispatch({
        ...(currentDoc !== proj.displayText
          ? { changes: { from: 0, to: currentDoc.length, insert: proj.displayText } }
          : {}),
        selection: EditorSelection.single(Math.max(0, Math.min(proj.displayText.length, dispHead))),
        effects: [refreshPrettyRecoveryUi.of(session.nonReplicaRevision)],
        annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
      });
    }
  } else if (session.nonReplicaProjection) {
    const prevProj = session.nonReplicaProjection;
    const rawHead = displayToSourceOffset(
      prevProj.sourceMap,
      view.state.selection.main.head,
      "right",
    );
    session.nonReplicaProjection = undefined;
    view.dispatch({ effects: collaboration.reconfigure([]) });
    const currentDoc = view.state.doc.toString();
    if (
      currentDoc !== session.nonReplicaRawText ||
      view.state.selection.main.head !== rawHead
    ) {
      view.dispatch({
        ...(currentDoc !== session.nonReplicaRawText
          ? {
              changes: {
                from: 0,
                to: currentDoc.length,
                insert: session.nonReplicaRawText,
              },
            }
          : {}),
        selection: EditorSelection.single(
          Math.max(0, Math.min(session.nonReplicaRawText.length, rawHead)),
        ),
        annotations: [documentSynchronization.of(true), Transaction.addToHistory.of(false)],
      });
    }
  }
}

/** Rebinding uses recovered text while preserving live navigation. */
export function rebindFilesCollaboration(
  view: EditorView,
  replica: DocumentReplica | undefined,
  options?: FilesCollaborationPrettyOptions,
): boolean {
  const existingSession = sessionsByView.get(view);
  const wantPretty =
    options?.enabled ??
    existingSession?.prettyEnabled ??
    false;
  const hasRecovery = Boolean(
    replica && hasActivePrettyRecoveryForDocument(replica.accepted.id, replica.incarnation),
  );

  if (wantPretty || hasRecovery || existingSession?.adapterAttached) {
    syncFilesCollaborationPrettyMode(view, replica, {
      ...existingSession,
      ...options,
      enabled: wantPretty,
    });
    return true;
  }

  if (options || existingSession) {
    const session = ensureSession(view, replica, options);
    if (replica && (!session.adapter || session.adapter.replica !== replica)) {
      session.adapter?.destroy();
      session.adapter = new PrettyReplicaEditorAdapter(replica, {
        path: session.path || replica.accepted.path,
        onAuthoritativeDocChange: (rawText, userEvent) => {
          session.onAuthoritativeDocChange?.(rawText, userEvent);
        },
      });
      session.adapter.setPrettyEnabled(false);
    } else if (!replica && session.adapter) {
      session.adapter.destroy();
      session.adapter = undefined;
      session.adapterAttached = false;
    }
  }

  if (!replica) {
    if (!view.state.facet(boundDocument)) return false;
    view.dispatch({ effects: collaboration.reconfigure([]) });
    return true;
  }
  const before = view.state.doc.toString(),
    text = replica.text.toString();
  if (view.state.facet(boundDocument) === replica.doc && before === text) return false;
  const changes = view.state.changes(
    diffSourceText(before, text).map((change) => ({
      from: change.fromA,
      to: change.toA,
      insert: text.slice(change.fromB, change.toB),
    })),
  );
  // Navigation during attachment takes precedence over the saved cursor.
  const selection = view.state.field(liveSelection)
    ? view.state.selection.map(changes)
    : replica.history.state.selection;
  view.dispatch({ effects: collaboration.reconfigure([]) });
  view.dispatch({
    changes,
    selection,
    annotations: Transaction.addToHistory.of(false),
  });
  view.dispatch({
    effects: collaboration.reconfigure([boundDocument.of(replica.doc), replica.extension]),
  });
  return true;
}

export function refreshFilesCollaborationEpoch(view: EditorView, replica: DocumentReplica): void {
  const bound = view.state.facet(boundDocument);
  if (bound && bound !== replica.doc) rebindFilesCollaboration(view, replica);
}

export function getFilesCollaborationPrettyAdapter(
  view: EditorView | undefined,
): PrettyReplicaEditorAdapter | undefined {
  return view ? sessionsByView.get(view)?.adapter : undefined;
}

export function getFilesCollaborationAuthoritativeDoc(
  view: EditorView | undefined,
): string | undefined {
  if (!view) return undefined;
  const session = sessionsByView.get(view);
  if (session?.adapter && (session.prettyEnabled || session.adapterAttached)) {
    return session.adapter.rawText;
  }
  if (session && !session.replica && session.prettyEnabled && session.nonReplicaProjection) {
    return session.nonReplicaRawText;
  }
  return view.state.doc.toString();
}

export function syncFilesCollaborationNonReplicaDraft(
  view: EditorView,
  nextRawDraft: string,
): boolean {
  const session = sessionsByView.get(view);
  if (!session || session.replica || !session.prettyEnabled || !session.nonReplicaProjection) {
    return false;
  }
  if (session.nonReplicaRawText === nextRawDraft) return true;
  session.nonReplicaRawText = nextRawDraft;
  syncFilesCollaborationPrettyMode(view, undefined, {
    ...session,
    enabled: session.prettyEnabled,
  });
  return true;
}

export function mapFilesCollaborationSourceLineColToDisplay(
  view: EditorView | undefined,
  line: number,
  col = 1,
): PrettyLineCol {
  const session = view ? sessionsByView.get(view) : undefined;
  if (session?.adapter && session.prettyEnabled) {
    return session.adapter.mapSourceLineColToDisplay(line, col);
  }
  if (session && !session.replica && session.prettyEnabled && session.nonReplicaProjection) {
    return sourceLineColToDisplay(
      session.nonReplicaProjection.sourceMap,
      session.nonReplicaRawText,
      line,
      col,
      "right",
    );
  }
  if (!view) return { line: Math.max(1, line), col: Math.max(1, col), offset: 0 };
  const clampedLine = Math.max(1, Math.min(view.state.doc.lines, Math.floor(line)));
  const docLine = view.state.doc.line(clampedLine);
  const clampedCol = Math.max(1, Math.min(Math.floor(col), docLine.length + 1));
  return {
    line: docLine.number,
    col: clampedCol,
    offset: docLine.from + clampedCol - 1,
  };
}

export function mapFilesCollaborationDisplayOffsetToSourceLineCol(
  view: EditorView | undefined,
  dispOffset: number,
): PrettyLineCol {
  const session = view ? sessionsByView.get(view) : undefined;
  if (session?.adapter && session.prettyEnabled) {
    return session.adapter.mapDisplayOffsetToSourceLineCol(dispOffset);
  }
  if (session && !session.replica && session.prettyEnabled && session.nonReplicaProjection) {
    return displayOffsetToSourceLineCol(
      session.nonReplicaProjection.sourceMap,
      session.nonReplicaRawText,
      dispOffset,
      "right",
    );
  }
  if (!view) return { line: 1, col: 1, offset: 0 };
  const clamped = Math.max(0, Math.min(view.state.doc.length, dispOffset));
  const lineObj = view.state.doc.lineAt(clamped);
  return {
    line: lineObj.number,
    col: clamped - lineObj.from + 1,
    offset: clamped,
  };
}

export function mapFilesCollaborationDisplayRangeToSource(
  view: EditorView | undefined,
  dispFrom: number,
  dispTo: number,
): { from: number; to: number } {
  const session = view ? sessionsByView.get(view) : undefined;
  const map =
    session?.adapter && session.prettyEnabled
      ? session.adapter.currentProjection.sourceMap
      : session && !session.replica && session.prettyEnabled && session.nonReplicaProjection
        ? session.nonReplicaProjection.sourceMap
        : undefined;
  if (!map) return { from: dispFrom, to: dispTo };
  const from = displayToSourceOffset(map, dispFrom, "right");
  const to = Math.max(from, displayToSourceOffset(map, dispTo, "left"));
  return { from, to };
}

export function projectSecretScreenForFilesView(
  view: EditorView | undefined,
  screen: SecretScreen | null,
): SecretScreen | null {
  if (!screen || !screen.spans || screen.spans.length === 0 || !view) return screen;
  const session = sessionsByView.get(view);
  if (!session || !session.prettyEnabled) return screen;
  const proj = session.adapter?.currentProjection ?? session.nonReplicaProjection;
  const rawText = session.adapter?.rawText ?? session.nonReplicaRawText;
  if (!proj) return screen;
  const dispRuneTable = createRuneOffsetTable(proj.displayText);
  const projectedSpans = mapRuneSpansToDisplay(proj.sourceMap, rawText, screen.spans).map((p) => ({
    ...p.span,
    start: dispRuneTable.utf16ToRuneOffset(p.dispFrom, "right"),
    end: dispRuneTable.utf16ToRuneOffset(p.dispTo, "left"),
  }));
  return { ...screen, spans: projectedSpans };
}

export function destroyFilesCollaborationPrettySession(
  view: EditorView | undefined,
  opts?: { clearCache?: boolean },
): void {
  if (!view) return;
  const session = sessionsByView.get(view);
  if (!session) return;
  const cacheIdentity =
    session.adapter?.replica.accepted.id ?? (session.key || session.path);
  session.adapter?.destroy();
  session.adapter = undefined;
  session.adapterAttached = false;
  sessionsByView.delete(view);
  if (opts?.clearCache && cacheIdentity) {
    clearPrettyProjectionCacheForIdentity(cacheIdentity);
  }
}
