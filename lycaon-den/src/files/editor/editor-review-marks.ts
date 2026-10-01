import type { FindingDiagnosticHandlers } from "../../components/source/annotations/findings-diagnostics.ts";
import type { FindingLineMark } from "../../components/source/annotations/knowing-gutter-model.ts";
import type { AttributionMark } from "../../components/source/annotations/knowing-gutter-model.ts";
import { createEffect, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { FilesEditorScope } from "../components/files-scope.ts";
import type { SecurityFinding, SourceAttributionResponse } from "../../api/types.ts";
import { setFindingDiagnostics } from "../../components/source/annotations/findings-diagnostics.ts";
import { findingsByLine, mergeAttributionIntervals } from "../../components/source/annotations/knowing-gutter-model.ts";
import { setLineGutterAttribution, setLineGutterFindings } from "../../components/source/annotations/line-gutter.ts";
import { applyEditorScopeDiff } from "../../components/source/diff/scope-diff.ts";
import { loadEditorAnnotations } from "./editor-annotations.ts";
import { clearFilesEditorFindings, setFilesEditorFindings } from "./files-editor-findings.ts";
import { tipSha } from "../review/review-model.ts";
import { scopeBaseline, scopeChangeKindFor, scopeComparisonTarget, scopeEffectFor } from "../tree/scope-resolution.ts";
import { comparisonEndpoints } from "../source/source-comparison.ts";
import type { EditorView } from "@codemirror/view";
import type { CodeScanEvent } from "../../api/types.ts";

type EditorComparisonOptions = FilesEditorScope & {
  editorScopeRevision: () => string;
  dropCondenseRequest: () => void;
  condenseIfRequested: (view: EditorView) => void;
};

export function observeEditorComparison(options: EditorComparisonOptions) {
  const { projectId: currentProjectId, sessionId: currentSessionId, client: currentClient, buffer, live, chromeReady,
    historicalActive, editorView, editorScopeRevision, dropCondenseRequest, condenseIfRequested } = options;
  const binding = { get view() { return options.currentView(); } };
  let presentedRange: { fileId: string; afterOrdinal: number } | null = null;
  let comparisonRequest: { client: LycaonClient; key: string; result: Promise<import("../../api/types.ts").SourceComparison> } | undefined;
  let displayedComparisonRevision: string | undefined;
  let displayedComparisonSHA: string | undefined;
  // Another window may acknowledge this file while this view is still reading.
  createEffect(() => {
    // Idle views release their retained comparison.
    const v = editorView();
    if (!live()) {
      presentedRange = null;
      comparisonRequest = undefined;
      displayedComparisonRevision = undefined;
      displayedComparisonSHA = undefined;
      if (v) applyEditorScopeDiff(v, null);
      return;
    }
    const comparisonRevision = editorScopeRevision();
    const c = currentClient();
    const path = buffer().path;
    const rootId = buffer().rootId;
    const sha = (buffer().baseSha256 ?? "").trim();
    const clear = () => {
      if (v) applyEditorScopeDiff(v, null);
    };
    if (
      !path ||
      !rootId ||
      buffer().kind !== "text" ||
      buffer().jobId ||
      historicalActive()
    ) {
      presentedRange = null;
      clear();
      dropCondenseRequest();
      return;
    }
    const changeKind = scopeChangeKindFor(currentProjectId(), rootId, path);
    if (changeKind === "deleted" || buffer().deleted != null || buffer().loadError != null) {
      presentedRange = null;
      clear();
      dropCondenseRequest();
      return;
    }
    if (!chromeReady() || !v) return;
    const change = scopeEffectFor(currentProjectId(), rootId, path);
    const baseline = scopeBaseline(currentProjectId());
    if (baseline !== "presentation" || (presentedRange && buffer().fileId && presentedRange.fileId !== buffer().fileId)) {
      presentedRange = null;
    }
    const held = presentedRange;
    if (!c || !baseline || (!change && !held) || !sha) {
      clear();
      dropCondenseRequest();
      return;
    }
    // Comparison marks map from saved text through unsaved edits.
    // Pending scope updates preserve the displayed comparison.
    if (change && sha !== tipSha(change.tip)) return;
    if (changeKind === "added" && !held) clear();
    const scopeTarget = scopeComparisonTarget(currentProjectId(), rootId, path);
    const target = held && "fileId" in scopeTarget
      ? { ...scopeTarget, fileId: held.fileId, presentationAfterOrdinal: held.afterOrdinal }
      : scopeTarget;
    const requestKey = JSON.stringify([currentProjectId(), currentSessionId(), target, sha, comparisonRevision]);
    if (comparisonRequest?.key !== requestKey || comparisonRequest.client !== c) {
      comparisonRequest = { client: c, key: requestKey, result: c.getProjectSourceComparison(currentProjectId(), target, { sessionId: currentSessionId() }) };
    }
    let cancelled = false;
    const request = comparisonRequest;
    void request.result.then(
        (d) => {
          if (cancelled || !binding.view) return;
          const endpoints = comparisonEndpoints(d);
          if (!endpoints) {
            clear();
            dropCondenseRequest();
            return;
          }
          const { before, after } = endpoints;
          if (baseline === "presentation" && d.file_id && d.presentation_after_ordinal != null) {
            presentedRange = { fileId: d.file_id, afterOrdinal: d.presentation_after_ordinal };
          }
          if (before.availability !== "available") {
            clear();
            dropCondenseRequest();
            return;
          }
          // A changed disk revision requires a fresh comparison.
          const head = after.sha256?.trim() || (change ? tipSha(change.tip) : "") || "";
          if (head !== (buffer().baseSha256 ?? "").trim()) return;
          applyEditorScopeDiff(binding.view, {
            original: before.content ?? "",
            tip: after.availability === "available" ? after.content ?? "" : null,
            attribution: d.attribution,
          });
          displayedComparisonRevision = comparisonRevision;
          displayedComparisonSHA = sha;
          condenseIfRequested(binding.view);
        },
        () => {
          if (comparisonRequest === request) comparisonRequest = undefined;
          if (!cancelled) {
            clear();
            dropCondenseRequest();
          }
        },
      );
    onCleanup(() => {
      cancelled = true;
    });
  });

  // Review opens can reuse the displayed comparison.
  createEffect(() => {
    if (!buffer().condenseContext || !chromeReady() || displayedComparisonRevision !== editorScopeRevision() || displayedComparisonSHA !== buffer().baseSha256) return;
    const v = editorView();
    if (v) condenseIfRequested(v);
  });

}

type EditorAnnotationsOptions = Omit<FilesEditorScope, "editorView"> & {
  scanUpdate: () => Pick<CodeScanEvent, "scan_id" | "status"> | undefined;
  setAttrMarks: (marks: AttributionMark[]) => unknown;
  setFindingMarks: (marks: FindingLineMark[]) => unknown;
  setFindingRecords: (findings: SecurityFinding[]) => unknown;
  setFindingsScanId: (id: string) => unknown;
  findingDiagnosticHandlers: FindingDiagnosticHandlers;
};

export function observeEditorAnnotations(options: EditorAnnotationsOptions) {
  const { projectId: currentProjectId, sessionId: currentSessionId, client: currentClient, buffer, live, chromeReady,
    historicalActive, scanUpdate, setAttrMarks, setFindingMarks, setFindingRecords,
    setFindingsScanId, findingDiagnosticHandlers } = options;
  const binding = { get view() { return options.currentView(); } };
  // Attribution follows lines in the active comparison.
  createEffect(() => {
    if (!live()) return;
    const c = currentClient();
    const projectId = currentProjectId();
    const sessionId = currentSessionId();
    void scanUpdate()?.scan_id;
    void scanUpdate()?.status;
    const path = buffer().path;
    const rootId = buffer().rootId;
    const sha = (buffer().baseSha256 ?? "").trim();
    // Typing carries the marks already shown; a save or scan refreshes them.
    const dirty = untrack(() => buffer().dirty);
    const kind = buffer().kind;
    if (
      !chromeReady() ||
      !c ||
      !path ||
      !rootId ||
      kind !== "text" ||
      buffer().jobId ||
      historicalActive()
    ) {
      setAttrMarks([]);
      setFindingMarks([]);
      setFindingRecords([]);
      setFindingsScanId("");
      clearFilesEditorFindings(buffer().key);
      if (binding.view) {
        setLineGutterAttribution(binding.view, []);
        setLineGutterFindings(binding.view, []);
        setFindingDiagnostics(binding.view, path, []);
      }
      return;
    }

    let cancelled = false;
    const applyHonest = (
      head: string,
      intervals: SourceAttributionResponse["intervals"],
      findings: SecurityFinding[],
      scanId: string,
    ) => {
      if (cancelled || untrack(() => buffer().dirty)) return;
      // Marks describe the saved text they were computed for.
      const honest = Boolean(sha) && head === sha;
      const nextAttr = honest ? mergeAttributionIntervals(intervals) : [];
      const nextFindings = honest ? findingsByLine(path, findings) : [];
      const nextRecords = honest ? findings : [];
      setAttrMarks(nextAttr);
      setFindingMarks(nextFindings);
      setFindingRecords(nextRecords);
      setFindingsScanId(scanId);
      setFilesEditorFindings(buffer().key, nextFindings);
      if (binding.view) {
        setLineGutterAttribution(binding.view, nextAttr);
        setLineGutterFindings(binding.view, nextFindings);
        setFindingDiagnostics(
          binding.view,
          path,
          nextRecords,
          findingDiagnosticHandlers,
        );
      }
    };

    if (dirty) return;
    if (!sha) {
      applyHonest("", [], [], "");
      return;
    }

    void loadEditorAnnotations(c, { projectId, rootId, path, sessionId }).then((result) => {
      if (cancelled) return;
      applyHonest(
        result.attribution.head_sha256 ?? "",
        result.attribution.intervals ?? [],
        result.findings,
        result.scanId,
      );
    });
    onCleanup(() => {
      cancelled = true;
    });
  });

}
