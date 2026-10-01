import { loadSourceComparison } from "../source/source-comparison-cache.ts";
import {
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  untrack,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  SourceStorage,
  SourceWalkEffect,
} from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { KeyedIndex } from "../../components/keyed-index.tsx";
import { fileVersionFromComparison, type FileVersionView } from "../history/file-version.ts";
import { ReviewFileRow as ReviewFileRowView } from "./ReviewFileRow.tsx";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { ReviewWalkContext } from "./ReviewWalkContext.tsx";
import { LycaonApiError } from "../../api/http.ts";
import type { ChatAttachmentRef } from "../../chat/composer/add-to-chat.ts";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import {
  buildReviewFileRows,
  buildReviewRows,
  buildSeenRows,
  LENS_NEEDS_CHAT_COPY,
  lensEmptyCopy,
  lensPickerLabel,
  lensScopeHeader,
  liveRowsInScope,
  reviewRowKey,
  type ReviewFileRow,
  type ReviewListRow,
  type ReviewSeenRow,
} from "./review-model.ts";
import { showMoreSeen } from "./review-pane.ts";
import { observeSurfaceFailure, reportSurfaceFailure, type SurfaceFailureCopy } from "../../notices/surface-failure.ts";

const REVIEWED_UNAVAILABLE: SurfaceFailureCopy = {
  code: "files_reviewed_unavailable",
  title: "Reviewed changes unavailable",
  suggestedAction: "Select the file again from the refreshed list.",
};
const UNSEEN_FAILED: SurfaceFailureCopy = {
  code: "files_unseen_failed",
  title: "Couldn’t mark file unseen",
  suggestedAction: "Mark it unseen again from the list.",
};
import {
  requestScopeResolve,
  resolvedScope,
  subscribeResolvedScope,
} from "../tree/scope-resolution.ts";
import { reviewLiveSnapshot, subscribeReviewLive } from "./review-live.ts";
import { subscribeWalk, walkState } from "../walk/walk-store.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { diffsBufferKey } from "../components/project-files-model.ts";
import { diffsAddressKey } from "./diffs-address.ts";
import { walkStepAt } from "../walk/walk-model.ts";

/** Readiness probe cadence while the ledger's pass runs: doubles to a ceiling. */
const INVENTORY_PROBE_MIN_MS = 1_000;
const INVENTORY_PROBE_MAX_MS = 30_000;

type Props = {
  projectId: string;
  client: LycaonClient | null;
  appStore: AppStore;
  onOpenFile: (
    rootId: string,
    path: string,
    version?: SourceWalkEffect,
    fileId?: string,
  ) => void;
  onViewReviewed?: (version: FileVersionView) => void;
  visible?: boolean;
  navigationRevision?: number;
  onRevertFile?: (rootId: string, path: string) => void;
  onRowMenu?: (
    anchor: { clientX: number; clientY: number },
    row: ReviewFileRow,
  ) => void;
};

export function ReviewLens(props: Props) {
  const [scopeTick, setScopeTick] = createSignal(0);
  const record = () => {
    void scopeTick();
    return resolvedScope(props.projectId);
  };
  // Copy names the comparison the rows answer, so a switch in flight never labels one list with another's name.
  const shown = createMemo(() => record().scope);
  const comparisonOff = createMemo(() => record().comparisonOff);
  const loading = () => record().status === "resolving";
  /** A resolved empty scope is distinct from an unresolved scope. */
  const held = () => record().settled;
  const error = () => record().error;
  // A hidden pane or a missing client never resolves; neither holds the stage.
  usePresentationParticipant(
    "review-lens",
    () =>
      props.visible === false ||
      !props.client ||
      !props.projectId.trim() ||
      record().settled ||
      Boolean(record().error),
  );
  const [storage, setStorage] = createSignal<SourceStorage | null>(null);
  const sourceStorage = () =>
    storage()?.storage.lanes.find((lane) => lane.lane === "source_blobs");
  const [selected, setSelected] = createSignal<string | null>(null);
  const [stepsOpen, setStepsOpen] = createSignal(false);
  const [liveTick, setLiveTick] = createSignal(0);
  const [walkTick, setWalkTick] = createSignal(0);
  const walk = createMemo(() => {
    void walkTick();
    return walkState(props.projectId);
  });

  onCleanup(
    subscribeWalk((id) => {
      if (id !== props.projectId.trim()) return;
      setSelected(null);
      setStepsOpen(false);
      setWalkTick((value) => value + 1);
    }),
  );

  const fileRows = createMemo(() => buildReviewFileRows(record().files));

  const live = createMemo(() => {
    void liveTick();
    return reviewLiveSnapshot(props.projectId);
  });

  const allRows = createMemo((): ReviewListRow[] => {
    const answer = record();
    const inScope = liveRowsInScope(live().rows, {
      scope: answer.scope,
      subject: answer.subject,
      turn: answer.turn,
      comparisonOff: answer.comparisonOff,
    });
    return buildReviewRows(fileRows(), inScope, live().stats);
  });

  const showsStats = createMemo(() =>
    allRows().some((row) => (row.added ?? 0) > 0 || (row.removed ?? 0) > 0),
  );

  onCleanup(subscribeResolvedScope((id) => {
    if (id === props.projectId.trim()) setScopeTick((n) => n + 1);
  }));

  onCleanup(subscribeReviewLive(props.projectId, () => setLiveTick((n) => n + 1)));

  /** The answer's chat is running a turn, so its list is still filling. */
  const turnRunning = createMemo(() => {
    const session = props.appStore.state.currentSession;
    return session?.status === "busy" && session.id === record().subject?.sessionId;
  });

  const copyOpts = () => ({
    turnRunning: turnRunning(),
    comparisonOff: comparisonOff(),
    subjectTitle: record().subject?.title,
  });

  const scopeHeader = createMemo(() => lensScopeHeader(shown(), copyOpts()));

  const emptyCopy = createMemo(() =>
    record().needsChat ? LENS_NEEDS_CHAT_COPY : lensEmptyCopy(shown(), copyOpts()));

  const diffsReason = createMemo(() => {
    if (comparisonOff()) return "Marking is off — choose a comparison first";
    if (allRows().length === 0) return "Nothing to read in this comparison";
    return `Open all diffs · ${lensPickerLabel(shown()).toLowerCase()}`;
  });
  const diffsOpen = createMemo(() => {
    const files = projectFilesState(props.projectId);
    const key = diffsBufferKey(diffsAddressKey({ kind: "lens" }));
    return (files.pendingKey ?? files.activeKey) === key;
  });
  const openAllDiffs = () => {
    if (comparisonOff() || allRows().length === 0) return;
    openFilesSurface({ kind: "diffs", projectId: props.projectId, address: { kind: "lens" } });
  };

  const incompleteInventory = () => {
    if (shown().kind === "commit") return undefined;
    const inventory = storage()?.inventory;
    return inventory && !inventory.complete ? inventory : undefined;
  };
  const inventoryNote = createMemo(() =>
    incompleteInventory()?.status === "error" ? null : incompleteInventory() ? "Indexing repository history in the background…" : null);
  observeSurfaceFailure(
    { code: "files_inventory_unavailable", title: "Repository inventory unavailable", suggestedAction: "Recorded changes remain available; reopen Review to refresh." },
    () => incompleteInventory()?.status === "error" && "Repository inventory could not be refreshed. Recorded changes remain available.",
    () => props.projectId,
  );

  const loadStorage = (c: LycaonClient) =>
    void c.getProjectSourceStorage(props.projectId).then(setStorage, () => undefined);

  // Showing the pane reads the comparison again; the files stage controls the request.
  createEffect(() => {
    const c = props.client;
    void props.projectId;
    if (props.visible === false || !c) return;
    untrack(() => {
      loadStorage(c);
      requestScopeResolve(props.projectId);
    });
  });

  const inventoryPending = createMemo(() => {
    const inventory = storage()?.inventory;
    return !!inventory && !inventory.complete && inventory.status !== "error";
  });
  // Poll inventory readiness; source batches refresh rows and completion refreshes the walk.
  createEffect(() => {
    if (props.visible === false || !inventoryPending()) return;
    const c = props.client;
    if (!c) return;
    let delay = INVENTORY_PROBE_MIN_MS;
    let timer: number | undefined;
    let disposed = false;
    const probe = () => {
      timer = window.setTimeout(() => {
        void c.getProjectSourceStorage(props.projectId).then(
          (next) => {
            if (disposed) return;
            setStorage(next);
            if (next.inventory.complete || next.inventory.status === "error") {
              requestScopeResolve(props.projectId);
              return;
            }
            delay = Math.min(delay * 2, INVENTORY_PROBE_MAX_MS);
            probe();
          },
          () => {
            if (disposed) return;
            delay = Math.min(delay * 2, INVENTORY_PROBE_MAX_MS);
            probe();
          },
        );
      }, delay);
    };
    probe();
    onCleanup(() => {
      disposed = true;
      if (timer !== undefined) window.clearTimeout(timer);
    });
  });

  let reviewedRequest = 0;
  const [loadingReviewed, setLoadingReviewed] = createSignal<string | null>(null);
  onCleanup(() => { reviewedRequest++; });
  createEffect(() => {
    void shown();
    void props.projectId;
    void props.visible;
    void props.navigationRevision;
    reviewedRequest++;
    setLoadingReviewed(null);
  });

  const openRow = (row: ReviewListRow, keepExpanded = false) => {
    reviewedRequest++;
    setLoadingReviewed(null);
    const key = rowKey(row);
    setStepsOpen(false);
    setSelected((prev) => (keepExpanded ? key : prev === key ? null : key));
    props.onOpenFile(row.rootId, row.path, undefined, row.fileId);
  };

  const openStep = (row: ReviewListRow, step: SourceWalkEffect) => {
    reviewedRequest++;
    setLoadingReviewed(null);
    props.onOpenFile(row.rootId, row.path, step, row.fileId);
  };

  const rowKey = (row: ReviewFileRow) => reviewRowKey(row.rootId, row.path);

  const chatRefFor = (row: ReviewListRow): ChatAttachmentRef | null =>
    props.projectId.trim() && row.rootId.trim() && row.path.trim()
      ? {
          kind: "path-file",
          projectId: props.projectId.trim(),
          rootId: row.rootId.trim(),
          path: row.path,
          name: row.basename,
        }
      : null;

  /** Files the reader already looked at; only the `new` scope has them. */
  const seenRows = createMemo((): ReviewSeenRow[] =>
    comparisonOff() || shown().kind !== "new" ? [] : buildSeenRows(record().seen),
  );
  const seenMore = () =>
    !comparisonOff() && shown().kind === "new" && record().seenMore;

  // A file that just left New is the one that animates into Seen.
  const seenArrivals = createMemo(
    (prev: { newKeys: ReadonlySet<string>; arrived: ReadonlySet<string> }) => {
      const newKeys = new Set(allRows().map(rowKey));
      const arrived = new Set(
        seenRows().map(rowKey).filter((key) => prev.newKeys.has(key)),
      );
      return { newKeys, arrived };
    },
    { newKeys: new Set<string>(), arrived: new Set<string>() },
  );

  createEffect(() => {
    if (props.visible !== false && seenRows().length > 0) {
      requestFirstTimeTip("review-seen");
    }
  });

  const viewReviewed = async (row: ReviewSeenRow) => {
    const c = props.client;
    if (!c || !props.onViewReviewed) return;
    const request = ++reviewedRequest;
    setLoadingReviewed(row.fileId);
    try {
      const comparison = await loadSourceComparison(c, props.projectId, {
        fileId: row.fileId, reviewedThroughOrdinal: row.throughOrdinal,
      });
      if (request !== reviewedRequest) return;
      const version = fileVersionFromComparison({
        fileId: row.fileId, versionId: comparison.after?.version_id ?? "",
        rootId: row.rootId, path: row.path, op: row.op, ts: row.seenAt,
        initialComparison: "before",
      }, comparison);
      if (!version) throw new Error("Reviewed endpoints unavailable");
      props.onViewReviewed({ ...version, reviewedThroughOrdinal: row.throughOrdinal });
    } catch (err) {
      if (request !== reviewedRequest) return;
      reportSurfaceFailure(REVIEWED_UNAVAILABLE, err instanceof LycaonApiError &&
        (err.code === "source_presentation_effect_changed" || err.code === "source_history_not_found")
        ? "This review has changed."
        : `Couldn’t load reviewed changes for ${row.basename}.`, props.projectId);
      requestScopeResolve(props.projectId);
    } finally {
      if (request === reviewedRequest) setLoadingReviewed(null);
    }
  };

  const markUnseen = async (row: ReviewSeenRow) => {
    const c = props.client;
    if (!c) return;
    reviewedRequest++;
    setLoadingReviewed(null);
    try {
      await c.withdrawProjectSourcePresentation(
        props.projectId,
        row.fileId,
        row.throughOrdinal,
      );
    } catch (err) {
      // A newer look replaced this one; the refreshed list shows where it stands.
      const superseded =
        err instanceof LycaonApiError &&
        (err.code === "source_presentation_effect_changed" || err.code === "source_presentation_not_found");
      if (!superseded) reportSurfaceFailure(UNSEEN_FAILED, `Couldn’t mark ${row.basename} unseen.`, props.projectId);
    }
    requestScopeResolve(props.projectId);
  };

  const onListKeyDown = (event: KeyboardEvent) => {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const visible: ReviewListRow[] = [...allRows(), ...seenRows()];
    if (visible.length === 0) return;
    event.preventDefault();
    const at = visible.findIndex((row) => rowKey(row) === selected());
    const step = event.key === "ArrowDown" ? 1 : -1;
    const next = visible[(at + step + visible.length) % visible.length];
    if (next) openRow(next, true);
  };

  return (
    <div class="den-review-lens" data-testid="review-lens">
      <Show
        when={walk().active}
        fallback={
          <>
            <div class="den-review-lens__section" data-testid="review-lens-scope">
              <div class="den-review-lens__header-row" data-files-ctx="no-menu">
                <h2
                  class="den-review-lens__scope-header"
                  data-testid="review-lens-scope-header"
                  data-tip={scopeHeader()}
                  data-tip-when-clipped
                >
                  {scopeHeader()}
                </h2>
                <Show when={allRows().length > 0}>
                  <span
                    class="den-review-lens__total"
                    data-testid="review-lens-total-count"
                  >
                    {allRows().length}
                  </span>
                </Show>
                <button
                  type="button"
                  class="den-review-lens__diffs den-icon-target"
                  data-testid="review-lens-open-diffs"
                  aria-label={diffsReason()}
                  aria-pressed={diffsOpen()}
                  data-tip={diffsReason()}
                  disabled={comparisonOff() || allRows().length === 0}
                  onClick={openAllDiffs}
                >
                  <ThemeIcon slot="diff" size={15} />
                </button>
              </div>

              <Show when={(loading() && !held()) || inventoryNote()}>
                <div class="den-review-lens__status">
                  <Show when={loading() && !held()}>
                    <p
                      class="den-review-lens__loading"
                      data-testid="review-lens-loading"
                    >
                      Loading…
                    </p>
                  </Show>
                  <Show when={inventoryNote()}>
                    <p
                      class="den-review-lens__loading"
                      data-testid="review-inventory-status"
                    >
                      {inventoryNote()}
                    </p>
                  </Show>
                </div>
              </Show>

              <div
                class="den-review-lens__list"
                data-testid="review-lens-list"
                aria-busy={loading() || undefined}
                tabindex={allRows().length > 0 ? 0 : undefined}
                onKeyDown={onListKeyDown}
              >
                {/* An unresolved scope claims nothing; only a settled read says "nothing new". */}
                <Show
                  when={allRows().length > 0}
                  fallback={
                    <Show when={held() && !error()}>
                      <p
                        class="den-review-lens__empty"
                        classList={{ "den-review-lens__empty--above-seen": seenRows().length > 0 }}
                        data-testid="review-lens-empty"
                      >
                        {emptyCopy()}
                      </p>
                    </Show>
                  }
                >
                  <KeyedIndex each={allRows()} keyOf={rowKey}>
                    {(row) => (
                      <ReviewFileRowView
                        row={row()}
                        chatRef={chatRefFor(row())}
                        reserveStat={showsStats()}
                        expanded={selected() === rowKey(row())}
                        stepsOpen={stepsOpen()}
                        onOpen={() => openRow(row())}
                        onRevert={
                          props.onRevertFile
                            ? () => props.onRevertFile?.(row().rootId, row().path)
                            : undefined
                        }
                        onToggleSteps={() => setStepsOpen((open) => !open)}
                        onOpenStep={(step) => openStep(row(), step)}
                        onRowMenu={(anchor) => props.onRowMenu?.(anchor, row())}
                      />
                    )}
                  </KeyedIndex>
                </Show>

                <Show when={seenRows().length > 0}>
                  <div
                    class="den-review-lens__group"
                    data-testid="review-lens-seen-heading"
                    data-files-ctx="no-menu"
                    data-first-time-tip-anchor="review-seen"
                  >
                    <h3 class="den-review-lens__group-title">Seen</h3>
                    <span
                      class="den-review-lens__total"
                      data-testid="review-lens-seen-count"
                    >
                      {seenMore() ? `${seenRows().length}+` : seenRows().length}
                    </span>
                  </div>
                  <KeyedIndex each={seenRows()} keyOf={rowKey}>
                    {(row) => (
                      <ReviewFileRowView
                        row={row()}
                        seen
                        arrived={seenArrivals().arrived.has(rowKey(row()))}
                        chatRef={chatRefFor(row())}
                        reserveStat={false}
                        expanded={selected() === rowKey(row())}
                        stepsOpen={stepsOpen()}
                        onOpen={() => openRow(row())}
                        onViewReviewed={props.client && props.onViewReviewed ? () => void viewReviewed(row()) : undefined}
                        loadingReviewed={loadingReviewed() === row().fileId}
                        onMarkUnseen={props.client ? () => void markUnseen(row()) : undefined}
                        onToggleSteps={() => setStepsOpen((open) => !open)}
                        onOpenStep={(step) => openStep(row(), step)}
                        onRowMenu={(anchor) => props.onRowMenu?.(anchor, row())}
                      />
                    )}
                  </KeyedIndex>
                  <Show when={seenMore()}>
                    <button
                      type="button"
                      class="den-review-lens__more"
                      data-testid="review-lens-seen-more"
                      onClick={() => showMoreSeen(props.projectId)}
                    >
                      Show older
                    </button>
                  </Show>
                </Show>
              </div>
            </div>

            <footer class="den-review-lens__footer" data-files-ctx="no-menu">
              <Show when={shown().kind === "commit"}>
                <span class="den-review-lens__storage">HEAD → Working files · Staged, unstaged, and untracked</span>
              </Show>
              <Show when={shown().kind !== "commit" && sourceStorage()}>
                {(status) => (
                  <span
                    class="den-review-lens__storage"
                    data-tip={`${Math.round(status().used_bytes / 1024 / 1024)} MB revision content retained locally`}
                  >
                    History retained locally
                  </span>
                )}
              </Show>
            </footer>
          </>
        }
      >
        <ReviewWalkContext
          step={walkStepAt(walk().walk, walk().at)}
          position={walk().at + 1}
          count={walk().walk.steps.length}
          loading={walk().status === "loading"}
          ready={walk().status === "ready"}
          comparison={walk().comparison}
          walkSteps={walk().walk.steps}
        />
      </Show>
    </div>
  );
}
