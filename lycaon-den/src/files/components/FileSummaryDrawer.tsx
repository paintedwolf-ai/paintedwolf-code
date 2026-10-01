import {
  Show,
  batch,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  untrack,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { isApiErrorCode } from "../../api/http.ts";
import type { ResizeSession } from "../../layout/resize-session.ts";
import type { ResizeAxis } from "../../components/primitives/ResizeHandle.tsx";
import {
  fileSummariesEnabled,
  fileSummariesSettingKnown,
} from "../../settings/editor/file-summary-settings.ts";
import { ResizeHandle } from "../../components/primitives/ResizeHandle.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import {
  getLiveFileBriefing,
  receiveFileBriefingSnapshot,
  subscribeFileBriefingResync,
  subscribeFileBriefings,
  type LiveFileBriefing,
} from "./file-briefing-live.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { FileSummaryPanel } from "./FileSummaryPanel.tsx";
import {
  briefingTargetIdentity,
  type FileBriefingSelection,
  type FileBriefingTarget,
} from "./file-briefing-target.ts";

type Props = {
  projectId: string;
  client: LycaonClient | null;
  selection: FileBriefingSelection;
  onOpenLocation: (
    target: FileBriefingTarget,
    line?: number,
  ) => void;
};

const HEIGHT_KEY = "paintedwolf.file-summary-height.v1";
const DEFAULT_HEIGHT = 260;
const MIN_HEIGHT = 160;
const openByProject = new Map<string, boolean>();

type FileSummaryView = {
  projectId: string;
  identity: string;
  target: FileBriefingTarget;
  contextLabel: string | null;
  summary: LiveFileBriefing | null;
  error: string | null;
};

type FileSummaryLoad = {
  displayPath: string;
  phase: "opening" | "summarizing";
  busy: boolean;
};

function briefingSettled(summary: LiveFileBriefing | null): boolean {
  return summary !== null &&
    summary.status !== "pending" &&
    summary.status !== "streaming";
}

function viewSettled(view: FileSummaryView | null): boolean {
  return view !== null &&
    (briefingSettled(view.summary) || view.error !== null);
}

function storedHeight(): number {
  if (typeof localStorage === "undefined") return DEFAULT_HEIGHT;
  const value = Number(localStorage.getItem(HEIGHT_KEY));
  return Number.isFinite(value) && value >= MIN_HEIGHT ? value : DEFAULT_HEIGHT;
}

function persistHeight(value: number): void {
  if (typeof localStorage === "undefined") return;
  try {
    localStorage.setItem(HEIGHT_KEY, String(Math.round(value)));
  } catch {}
}

export function FileSummaryDrawer(props: Props) {
  const projectId = () => props.projectId.trim();
  const [open, setOpen] = createSignal(openByProject.get(projectId()) ?? false);
  const [height, setHeight] = createSignal(storedHeight());
  const [view, setView] = createSignal<FileSummaryView | null>(null);
  const [lastSettledView, setLastSettledView] =
    createSignal<FileSummaryView | null>(null);
  const [activeLoad, setActiveLoad] = createSignal<FileSummaryLoad | null>(null);
  const [manualRequest, setManualRequest] = createSignal<{
    identity: string;
    sequence: number;
  } | null>(null);
  let manualSequence = 0;
  let consumedManualSequence = 0;
  let hostEl: HTMLDivElement | undefined;

  const setDrawerOpen = (value: boolean) => {
    openByProject.set(projectId(), value);
    setOpen(value);
  };

  const showView = (
    id: string,
    identity: string,
    target: FileBriefingTarget,
    contextLabel: string | null,
    summary: LiveFileBriefing | null,
    requestError: string | null = null,
  ) => {
    const next: FileSummaryView = {
      projectId: id,
      identity,
      target,
      contextLabel,
      summary,
      error: requestError,
    };
    setView(next);
    if (viewSettled(next)) setLastSettledView(next);
  };

  const summaryFailure = createMemo(() => {
    const current = view();
    if (!current) return null;
    if (current.error) return current.error;
    return current.summary?.status === "failed"
      ? current.summary.error || "The summary could not be generated."
      : null;
  });
  observeSurfaceFailure(
    {
      code: "file_summary_failed",
      title: "Could not summarize the file",
      suggestedAction: "Use the summary action in the summary drawer to try again, or continue without a summary.",
    },
    summaryFailure,
    projectId,
  );

  const busy = () => activeLoad()?.busy === true;
  const retainingView = () => busy() && viewSettled(view());
  const panelLoading = () => busy() && !retainingView();

  createEffect(() => {
    if (!fileSummariesSettingKnown() || !fileSummariesEnabled()) {
      setDrawerOpen(false);
      setView(null);
      setLastSettledView(null);
      setActiveLoad(null);
      return;
    }
    const id = projectId();
    const selection = props.selection;
    const client = props.client;
    const requestedManual = manualRequest();
    if (untrack(view)?.projectId !== id) setView(null);
    if (untrack(lastSettledView)?.projectId !== id) setLastSettledView(null);
    if (!open()) {
      setActiveLoad(null);
      return;
    }
    if (selection.availability === "loading") {
      const retained = untrack(lastSettledView);
      if (retained?.projectId === id) setView(retained);
      else setView(null);
      setActiveLoad({
        displayPath: selection.displayPath ?? "this file",
        phase: "opening",
        busy: true,
      });
      return;
    }
    if (selection.availability !== "ready") {
      setView(null);
      setActiveLoad(null);
      return;
    }
    const target = selection.target;
    const identity = briefingTargetIdentity(target);
    if (!client) {
      showView(
        id,
        identity,
        target,
        selection.contextLabel,
        null,
        "File summary is unavailable while reconnecting.",
      );
      setActiveLoad(null);
      return;
    }
    const manual =
      requestedManual?.identity === identity &&
      requestedManual.sequence > consumedManualSequence;
    if (manual) consumedManualSequence = requestedManual.sequence;

    let cancelled = false;
    const existing = getLiveFileBriefing(id, identity);
    let expectedTargetKey = existing?.target_key ?? "";
    const existingSettled = briefingSettled(existing);
    const retained = untrack(lastSettledView);
    if (existingSettled) {
      showView(id, identity, target, selection.contextLabel, existing);
    } else if (retained?.projectId === id) {
      setView(retained);
    } else {
      showView(id, identity, target, selection.contextLabel, existing);
    }
    setActiveLoad({
      displayPath: selection.displayPath ?? target.path,
      phase: "summarizing",
      busy: manual || !existingSettled,
    });

    const receive = (value: LiveFileBriefing) => {
      if (cancelled) return;
      if (briefingSettled(value)) {
        batch(() => {
          showView(id, identity, target, selection.contextLabel, value);
          setActiveLoad(null);
        });
        return;
      }
      setActiveLoad({
        displayPath: selection.displayPath ?? target.path,
        phase: "summarizing",
        busy: true,
      });
      if (!viewSettled(untrack(view))) {
        showView(id, identity, target, selection.contextLabel, value);
      }
    };

    const unsubscribe = subscribeFileBriefings(
      (pid, rootId, path, value) => {
        if (
          !cancelled &&
          pid === projectId() &&
          rootId === target.root_id &&
          path === target.path &&
          expectedTargetKey !== "" &&
          value.target_key === expectedTargetKey
        ) {
          receive(value);
        }
      },
    );

    const request = async (trigger: "automatic" | "manual") => {
      try {
        let result;
        if (trigger === "manual") {
          result = await client.requestFileBriefing(id, {
            ...target,
            trigger,
          });
        } else {
          try {
            result = await client.getFileBriefing(id, target);
          } catch (requestError) {
            if (!isApiErrorCode(requestError, ["file_briefing_not_found"])) {
              throw requestError;
            }
            result = await client.requestFileBriefing(id, {
              ...target,
              trigger,
            });
          }
        }
        if (cancelled) return;
        expectedTargetKey = result.target_key;
        receive(receiveFileBriefingSnapshot(id, result, identity));
      } catch (requestError) {
        if (!cancelled) {
          const current = untrack(view);
          batch(() => {
            showView(
              id,
              identity,
              target,
              selection.contextLabel,
              current?.identity === identity ? current.summary : null,
              requestError instanceof Error
                ? requestError.message
                : "Could not summarize this file.",
            );
            setActiveLoad(null);
          });
        }
      }
    };

    const resync = subscribeFileBriefingResync(() => {
      void client
        .getFileBriefing(id, target)
        .then((result) => {
          if (cancelled) return;
          expectedTargetKey = result.target_key;
          receive(receiveFileBriefingSnapshot(id, result, identity));
        })
        .catch(() => undefined);
    });

    void request(manual ? "manual" : "automatic");
    onCleanup(() => {
      cancelled = true;
      unsubscribe();
      resync();
    });
  });

  const maximumHeight = () =>
    Math.max(MIN_HEIGHT, Math.floor((hostEl?.parentElement?.clientHeight ?? 600) * 0.72));
  const clampHeight = (value: number) =>
    Math.max(MIN_HEIGHT, Math.min(maximumHeight(), Math.round(value)));
  const beginResize = (_axis: ResizeAxis, start: number): ResizeSession => {
    let preview = start;
    return {
      preview: (value) => {
        preview = clampHeight(value);
        setHeight(preview);
      },
      commit: () => persistHeight(preview),
      cancel: () => setHeight(start),
    };
  };
  const resetHeight = () => {
    const next = clampHeight(DEFAULT_HEIGHT);
    setHeight(next);
    persistHeight(next);
  };

  const retry = () => {
    const target = props.selection.target;
    if (target) {
      manualSequence += 1;
      setManualRequest({
        identity: briefingTargetIdentity(target),
        sequence: manualSequence,
      });
    }
  };

  return (
    <Show when={fileSummariesSettingKnown() && fileSummariesEnabled()}>
      <div
        class="den-file-summary-drawer"
        data-open={open() ? "true" : "false"}
        data-testid="file-summary-drawer"
        ref={(element) => {
          hostEl = element;
        }}
        style={open() ? { height: `${clampHeight(height())}px` } : undefined}
      >
        <Show when={open()}>
          <ResizeHandle
            class="den-file-summary-drawer__resize"
            axis="height"
            direction={-1}
            size={() => height()}
            clamp={(value) => clampHeight(value)}
            onBegin={beginResize}
            onReset={resetHeight}
            rootClass="den-file-summary-resizing"
            ariaLabel="Resize file summary"
            ariaMin={() => MIN_HEIGHT}
            ariaMax={maximumHeight}
            tip="Drag to resize the file summary — double-click to reset"
            testId="file-summary-resize"
          />
        </Show>
        <button
          type="button"
          class="den-file-summary-drawer__bar"
          aria-expanded={open()}
          aria-controls="file-summary-drawer-content"
          data-testid="file-summary-toggle"
          onClick={() => setDrawerOpen(!open())}
        >
          <span>File summary</span>
          <span class="den-file-summary-drawer__path">
            {props.selection.displayPath ?? "Open a file to summarize"}
          </span>
          <span aria-hidden="true">{open() ? "⌄" : "⌃"}</span>
        </button>
        <Show when={open()}>
          <Scrollport
            id="file-summary-drawer-content"
            class="den-file-summary-drawer__body"
            contentClass="den-file-summary-drawer__content"
            data-testid="file-summary-scroll"
            aria-busy={busy() ? true : undefined}
          >
            <Show when={retainingView() ? activeLoad() : null} keyed>
              {(loading) => (
                <span
                  class="sr-only"
                  role="status"
                  aria-live="polite"
                  aria-atomic="true"
                  data-testid="file-summary-status"
                >
                  {loading.phase === "opening" ? "Loading" : "Summarizing"}{" "}
                  {loading.displayPath}…
                </span>
              )}
            </Show>
            <Show
              when={view()}
              keyed
              fallback={
                <p
                  class="den-file-summary-drawer__empty"
                  role={props.selection.availability === "loading" ? "status" : undefined}
                >
                  <Show when={props.selection.contextLabel}>
                    <span class="den-file-summary__context">
                      {props.selection.contextLabel}
                    </span>
                  </Show>
                  {props.selection.unavailableReason ??
                    "Open a file to generate its summary."}
                </p>
              }
            >
              {(visible) => (
                <div
                  class="den-retained-presentation"
                  data-retained={retainingView() ? "true" : "false"}
                  aria-hidden={retainingView() ? "true" : undefined}
                  inert={retainingView() ? true : undefined}
                >
                  <FileSummaryPanel
                    target={visible.target}
                    summary={visible.summary}
                    requestFailed={visible.error !== null}
                    contextLabel={visible.contextLabel}
                    loading={panelLoading()}
                    onRetry={retry}
                    onOpenLocation={props.onOpenLocation}
                  />
                </div>
              )}
            </Show>
          </Scrollport>
        </Show>
      </div>
    </Show>
  );
}

export function resetFileSummaryDrawerForTests(): void {
  openByProject.clear();
  if (typeof localStorage !== "undefined") localStorage.removeItem(HEIGHT_KEY);
}
