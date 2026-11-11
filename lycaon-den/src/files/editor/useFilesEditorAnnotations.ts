import { closeCompletion } from "@codemirror/autocomplete";
import { createEffect, createMemo, createSignal, on, onCleanup } from "solid-js";
import type { CodeScanEvent, SecurityFinding, SourceDefinitionCandidate } from "../../api/types.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import {
  type AttributionMark,
  type FindingLineMark,
} from "../../components/source/annotations/knowing-gutter-model.ts";
import { languageLabelForPath } from "../../components/source/editor/codemirror-lang.ts";
import { enclosingSymbolAt } from "../../components/source/inline-edit/inline-edit-scope.ts";
import {
  secretScreenSummary,
  type SecretScreenGap,
} from "../../components/source/secrets/secret-span-model.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { createPresentationWaiting } from "../../ui/presentation.ts";
import { useResidentPresence } from "../../ui/resident-presence-context.tsx";
import type { FilesEditorScope } from "../components/files-scope.ts";
import { editorDocumentScreenStatus } from "../documents/editor-document.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { filesBufferText } from "../documents/project-files-buffers.ts";
import { fileVersionIsReadable, type FileVersionView } from "../history/file-version.ts";
import type { DiffLineHunk } from "../review/line-diff.ts";
import { createEditorLineFacts } from "./editor-line-facts.ts";
import { observeEditorAnnotations } from "./editor-review-marks.ts";
import { createEditorSymbolNavigation } from "./editor-symbol-navigation.tsx";
import { setFilesEditorPresented } from "./files-editor-host.ts";
import { filesEditorSecretScreen } from "./files-editor-secret-screen.ts";

export function useFilesEditorAnnotations(
  options: FilesEditorScope & {
    version: () => FileVersionView | null;
    initialMarkdownView: () => "code" | "preview" | undefined;
    cursorLine: () => number;
    scanUpdate: () => Pick<CodeScanEvent, "scan_id" | "status"> | undefined;
    revealInTranscript: () => ((target: TranscriptRevealTarget) => void) | undefined;
    rejectHunk: () => ((hunk: DiffLineHunk) => void) | undefined;
    onSymbolJump: (line: number) => void;
    definitionPicker: () => {
      symbol: string;
      candidates: readonly SourceDefinitionCandidate[];
      truncated: boolean;
      selected: number;
    } | null;
    currentAbsent: () => boolean;
    lastDeletionKind: () => "pending" | "deleted" | "unrecorded";
    deletionReady: () => boolean;
    historyPending: () => boolean;
    versionDisplayed: () => boolean;
  },
) {
  const presence = useResidentPresence();
  const [mdView, setMdView] = createSignal<"code" | "preview">(
    options.initialMarkdownView() ?? "code",
  );
  const [previewText, setPreviewText] = createSignal("");
  const [attrMarks, setAttrMarks] = createSignal<AttributionMark[]>([]);
  const [findingMarks, setFindingMarks] = createSignal<FindingLineMark[]>([]);
  // Finding records preserve underline columns omitted by gutter marks.
  const [findingRecords, setFindingRecords] = createSignal<SecurityFinding[]>([]);
  const [findingsScanId, setFindingsScanId] = createSignal("");

  const lineFacts = createEditorLineFacts({
    ...options,
    cursorLine: options.cursorLine,
    findingsScanId,
    revealInTranscript: options.revealInTranscript,
    rejectHunk: options.rejectHunk,
  });
  onCleanup(lineFacts.clearLineFactsTimers);
  lineFacts.observePresence();
  onCleanup(lineFacts.hideLineFacts);
  onCleanup(lineFacts.clearRemovedTimers);

  const symbolNavigation = createEditorSymbolNavigation({
    ...options,
    onSymbolJump: options.onSymbolJump,
  });
  const {
    symbolMenuOpen,
    symbols,
    closeSymbolMenu,
    toggleSymbolMenu,
    observeSource: observeSymbolSource,
  } = symbolNavigation;

  createEffect(() => {
    if (options.definitionPicker() != null) {
      closeSymbolMenu("keep");
      lineFacts.hideLineFacts();
      const v = options.currentView();
      if (v) closeCompletion(v);
    }
  });

  observeSymbolSource();

  /** The breadcrumb ends with the innermost symbol containing the caret. */
  const enclosingCrumbSymbol = createMemo(() => {
    const rows = symbols();
    if (rows.length === 0) return null;
    const caret = options.cursorLine();
    const docLines = options.currentView()?.state.doc.lines ?? caret;
    const enclosing = enclosingSymbolAt(
      rows.map((s) => ({ name: s.name, line: s.line })),
      caret,
      docLines,
    );
    if (!enclosing) return null;
    const kind =
      rows.find((s) => s.name === enclosing.name && s.line === enclosing.startLine)?.kind ?? "";
    return { ...enclosing, kind };
  });

  createEffect(
    on(
      () => options.buffer().key,
      () => closeSymbolMenu("keep"),
      { defer: true },
    ),
  );

  const languageLabel = createMemo(() =>
    languageLabelForPath(options.version()?.path ?? options.buffer().path),
  );
  const isMarkdown = () => languageLabel() === "Markdown";
  const previewActive = () => isMarkdown() && mdView() === "preview";

  createEffect(() => {
    options.editorView();
    setFilesEditorPresented(
      options.projectId(),
      options.buffer().key,
      presence() === "active" &&
        !previewActive() &&
        !options.historicalActive() &&
        !options.currentAbsent(),
    );
  });

  const [previewReady, setPreviewReady] = createSignal(false);
  const [previewDisplayed, setPreviewDisplayed] = createSignal(false);
  const previewPending = () => previewActive() && !previewReady();
  const showPreviewWait = createPresentationWaiting(
    () => previewPending() || (options.historyPending() && !options.versionDisplayed()),
  );

  usePresentationParticipant("file-document", () => {
    if (options.buffer().loadError) return true;
    if (previewActive()) return previewReady();
    // Historical rows register preparation through their own deck.
    if (options.historicalActive()) return true;
    if (options.currentAbsent() && !options.buffer().loading) {
      return options.lastDeletionKind() === "deleted"
        ? options.deletionReady()
        : options.lastDeletionKind() === "unrecorded";
    }
    return (
      !options.buffer().loading &&
      (options.currentAbsent() || (options.chromeReady() && !options.buffer().condenseContext))
    );
  });

  createEffect(() => {
    if (!previewActive()) {
      setPreviewReady(false);
      setPreviewDisplayed(false);
    }
  });

  createEffect(() => {
    if (previewActive() && !options.buffer().loading && !options.buffer().loadError) {
      setPreviewText(filesBufferText(options.buffer()));
    } else if (!previewActive()) {
      setPreviewText("");
    }
  });

  createEffect(
    on(
      previewActive,
      (active) => {
        if (active) return;
        const v = options.currentView();
        if (!v) return;
        v.requestMeasure();
        if (typeof requestAnimationFrame === "function") {
          requestAnimationFrame(() => v.requestMeasure());
        }
      },
      { defer: true },
    ),
  );

  observeEditorAnnotations({
    ...options,
    scanUpdate: options.scanUpdate,
    setAttrMarks,
    setFindingMarks,
    setFindingRecords,
    setFindingsScanId,
    findingDiagnosticHandlers: lineFacts.findingDiagnosticHandlers,
  });

  /** Permanent chrome shows incomplete coverage only. */
  const secretScreenGap = createMemo<SecretScreenGap | null>(() => {
    const buffer = options.buffer();
    if (buffer.loading || buffer.kind !== "text") return null;
    if (options.historicalActive()) {
      const ver = options.version();
      if (!ver || !fileVersionIsReadable(ver)) return null;
      return secretScreenSummary(ver.secretScreen).gap;
    }
    if (buffer.deleted?.previous.availability === "available") {
      return secretScreenSummary(buffer.deleted.previous.secret_screen).gap;
    }
    if (!buffer.documentId) return null;
    if (editorDocumentScreenStatus(buffer.documentId) === "pending") return null;
    return secretScreenSummary(filesEditorSecretScreen(buffer.key)).gap;
  });

  const filesState = () => projectFilesState(options.projectId());
  const dirtyOpenCount = () =>
    filesState().order.filter((k) => filesState().byKey[k]?.dirty).length;

  return {
    mdView,
    setMdView,
    previewText,
    setPreviewText,
    previewReady,
    setPreviewReady,
    previewDisplayed,
    setPreviewDisplayed,
    languageLabel,
    isMarkdown,
    previewActive,
    previewPending,
    showPreviewWait,
    attrMarks,
    findingMarks,
    findingRecords,
    lineFacts,
    symbolNavigation,
    symbolMenuOpen,
    symbols,
    toggleSymbolMenu,
    enclosingCrumbSymbol,
    secretScreenGap,
    dirtyOpenCount,
  };
}
