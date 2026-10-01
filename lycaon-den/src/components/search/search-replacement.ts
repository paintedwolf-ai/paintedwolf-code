import { createSearchRefresh } from "../../search/search-refresh.ts";
import { createEffect, batch, createSignal, on } from "solid-js";
import type { SearchReplacePreviewRequest, SearchReplacePreviewResponse } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { lintFromApiError, type QueryLintError } from "../../search/search-query-model.ts";
import {
  reconcileReplaceSelection,
  selectionToApplyFiles,
  type ReplaceSelection,
} from "../../search/search-replace-selection.ts";
import { refreshBuffersAfterReplace } from "../../search/search-replace-buffers.ts";
import { type ReplaceApplySummary } from "./SearchReplacePreview.tsx";

type SearchReplacementOptions = {
  originProjectId: () => string | null;
  query: () => string;
  replaceMode: () => boolean;
  residentLive: () => boolean;
  renameFrom: () => string | null;
  replaceRequest: () => SearchReplacePreviewRequest;
  previewRequestKey: () => string;
  previewCurrent: () => boolean;
  setApiLint: (lint: QueryLintError | null) => void;
  setPreviewLint: (lint: QueryLintError | null) => void;
  reportError: (error: unknown) => void;
  runSearch: (query: string) => void;
};

export function createSearchReplacement({ originProjectId, query, replaceMode, residentLive,
  renameFrom, replaceRequest, previewRequestKey, previewCurrent, setApiLint, setPreviewLint,
  reportError, runSearch }: SearchReplacementOptions) {
  const [replacePreview, setReplacePreview] =
    createSignal<SearchReplacePreviewResponse | null>(null);
  const [reviewedRequest, setReviewedRequest] = createSignal<{
    key: string;
    request: SearchReplacePreviewRequest;
  } | null>(null);
  const [replaceSelection, setReplaceSelection] =
    createSignal<ReplaceSelection | null>(null);
  const [replaceApplying, setReplaceApplying] = createSignal(false);
  const [replaceSummary, setReplaceSummary] =
    createSignal<ReplaceApplySummary | null>(null);

  let replaceGeneration = 0;
  let replaceController: AbortController | undefined;
  const replaceRefresh = createSearchRefresh();
  let previewTimer: ReturnType<typeof setTimeout> | undefined;
  const [previewPreparing, setPreviewPreparing] = createSignal(false);
  const [previewNotice, setPreviewNotice] = createSignal("");
  const stopPreview = () => {
    clearTimeout(previewTimer);
    replaceGeneration++;
    replaceController?.abort();
    replaceRefresh.reset();
    setPreviewPreparing(false);
  };
  const cancelPreview = () => {
    stopPreview();
    setReviewedRequest(null);
    setPreviewNotice("Preview preparation cancelled.");
  };
  const runReplacePreview = async () => {
    if (replaceApplying() || !residentLive()) return;
    clearTimeout(previewTimer);
    replaceController?.abort();
    const controller = new AbortController();
    replaceController = controller;
    const generation = ++replaceGeneration;
    const request = replaceRequest();
    const key = previewRequestKey();
    const current = () => generation === replaceGeneration && key === previewRequestKey();
    setReviewedRequest(null);
    if (!request.query || !replaceMode()) {
      setPreviewPreparing(false);
      setPreviewNotice("Enter a search query to prepare replacements.");
      setReplacePreview(null);
      setReplaceSelection(null);
      return;
    }
    const client = getLycaonClient();
    if (!client) {
      setPreviewPreparing(false);
      setPreviewNotice("Connect to the host to prepare replacements.");
      return;
    }
    setPreviewPreparing(true);
    setPreviewNotice("");
    try {
      const result = await client.previewSearchReplacement(request, controller.signal);
      if (!current()) return;
      replaceRefresh.clear();
      if (result.state === "preparing") {
        setPreviewNotice("Discovering files for the replacement preview…");
        replaceRefresh.schedule(result, () => {
          if (current() && residentLive()) void runReplacePreview();
        });
        return;
      }
      replaceRefresh.reset();
      setPreviewPreparing(false);
      setPreviewNotice("");
      // Preserve curated hunks across preview refreshes.
      batch(() => {
        setReplaceSelection(
          reconcileReplaceSelection(
            replaceSelection(),
            replacePreview()?.files ?? [],
            result.files ?? [],
          ),
        );
        setReplacePreview(result);
        setReviewedRequest({ key, request });
        setReplaceSummary(null);
        setPreviewLint(null);
      });
    } catch (err) {
      if (!current()) return;
      replaceRefresh.reset();
      setPreviewPreparing(false);
      setPreviewNotice("The replacement preview could not be prepared. Try again.");
      if (
        err instanceof LycaonApiError &&
        (err.code === "search_query_invalid" ||
          err.code === "search_pattern_invalid")
      ) {
        setPreviewLint(lintFromApiError(err));
        setReplacePreview(null);
        setReplaceSelection(null);
      } else {
        reportError(err);
      }
    }
  };

  const schedulePreview = () => {
    clearTimeout(previewTimer);
    setPreviewPreparing(Boolean(replaceMode() && query().trim() && residentLive()));
    previewTimer = setTimeout(() => void runReplacePreview(), 280);
  };

  const applyReplaceFiles = async (
    files: ReturnType<typeof selectionToApplyFiles>,
  ) => {
    const client = getLycaonClient();
    const projectId = originProjectId();
    const reviewed = reviewedRequest();
    if (!client || !projectId || files.length === 0 || replaceApplying() || !reviewed || !previewCurrent()) return;
    const appliedRenameFrom = renameFrom();
    replaceGeneration += 1;
    setReviewedRequest(null);
    setReplaceApplying(true);
    let confirmed = false;
    try {
      const result = await client.applySearchReplacement({
        ...reviewed.request,
        operation_id: crypto.randomUUID(),
        origin_project_id: projectId,
        files,
      });
      confirmed = true;
      const applied = (result.files ?? []).filter((f) => f.applied);
      const skipped = (result.files ?? []).filter((f) => f.skipped);
      const appliedMatches = applied.reduce(
        (n, f) => n + (f.matches ?? 0),
        0,
      );
      setReplaceSummary({
        appliedMatches,
        appliedFiles: applied.length,
        skipped,
        batchId: result.batch_id,
        applied: applied.map((f) => ({ root_id: f.root_id, path: f.path })),
        renameFrom: appliedRenameFrom,
        replacement: reviewed.request.replacement,
      });
      await refreshBuffersAfterReplace(client, projectId, applied);
      void runSearch(query());
    } catch (err) {
      if (
        err instanceof LycaonApiError &&
        (err.code === "search_query_invalid" ||
          err.code === "search_pattern_invalid")
      ) {
        setApiLint({
          offset: 0,
          kind: "syntax",
          message: err.message,
        });
      } else {
        reportError(err);
      }
    } finally {
      setReplaceApplying(false);
      if (!confirmed || reviewed.key !== previewRequestKey()) schedulePreview();
    }
  };

  const observePreviewChanges = () => {
    createEffect(
      on(previewRequestKey, () => {
        stopPreview();
        setReviewedRequest(null);
        setPreviewNotice("");
        if (!replaceMode()) return;
        schedulePreview();
      }),
    );
  };
  return { replacePreview, setReplacePreview, reviewedRequest, replaceSelection, setReplaceSelection,
    replaceApplying, replaceSummary, setReplaceSummary, previewPreparing, previewNotice,
    stopPreview, cancelPreview, runReplacePreview, applyReplaceFiles, observePreviewChanges };
}
