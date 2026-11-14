import { createSurfaceQuery } from "../../ui/surface-query.ts";
import {
  Match,
  Show,
  Switch,
  createEffect,
  createMemo,
  createSignal,
  on,
  onCleanup,
  onMount,
} from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import type { LycaonClient } from "../../api/client.ts";
import type { ArtifactListItem, ArtifactListResponse } from "../../api/types.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { KeyedIndex } from "../keyed-index.tsx";
import { BrowseSegmented } from "../browse/BrowseSegmented.tsx";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import { TablePager } from "../list/TablePager.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { DenOverlay } from "../overlay/DenOverlay.tsx";
import { ChromeCloseButton } from "../shell/ChromeCloseButton.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { isVisualVideoMime } from "../../chat/visual/visual-artifact-model.ts";
import { isFilmstripMime } from "../../chat/visual/filmstrip-zip.ts";
import { isTimelineMime } from "../../chat/visual/timeline-archive.ts";
import { TranscriptVisualFilmstrip } from "../transcript/TranscriptVisualFilmstrip.tsx";
import { TranscriptVisualTimeline } from "../transcript/TranscriptVisualTimeline.tsx";
import { artifactTranscriptAnchor } from "../../chat/visual/artifact-transcript-anchor.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import {
  type ArtifactChatFilter,
  type ArtifactSourceFilter,
  artifactSourceLabel,
  shortChatLabel,
} from "./project-artifacts-filter.ts";
import { ArtifactGalleryTile } from "./ArtifactGalleryTile.tsx";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import {
  createArtifactRefreshSubscription,
  notifyArtifactChanged,
} from "../../chat/visual/artifact-change-store.ts";
import { deleteConfirmBody } from "../../files/components/artifact-reference-impact.ts";
import { createResidentActivity } from "../../ui/resident-activity.ts";

type Props = {
  projectId: string;
  appStore: AppStore;
  client?: LycaonClient | null;
  back?: import("../shell/StageBackChip.tsx").StageBack | null;
  onRevealInTranscript?: (target: TranscriptRevealTarget) => void;
};

const ARTIFACT_SOURCE_OPTIONS = [
  { id: "all", label: "All", testId: "project-artifacts-source-all" },
  { id: "render", label: "render", testId: "project-artifacts-source-render" },
  { id: "capture", label: "capture", testId: "project-artifacts-source-capture" },
  { id: "fetch", label: "fetch", testId: "project-artifacts-source-fetch" },
  { id: "user", label: "user", testId: "project-artifacts-source-user" },
  { id: "workspace", label: "workspace", testId: "project-artifacts-source-workspace" },
] as const;

const ARTIFACTS_PAGE_SIZE = 12;

export function ProjectArtifactsView(props: Props) {
  onMount(() => requestFirstTimeTip("project-artifacts"));

  const client = () => props.client ?? getLycaonClient() ?? null;
  const [sourceFilter, setSourceFilter] =
    createSignal<ArtifactSourceFilter>("all");
  const [chatFilter, setChatFilter] = createSignal<ArtifactChatFilter>("all");
  const [lightboxIndex, setLightboxIndex] = createSignal<number | null>(null);
  // Tiles report and clear their own URLs on mount, replacement, and unmount.
  const [srcById, setSrcById] = createSignal<Record<string, string>>({});

  const [pendingDelete, setPendingDelete] =
    createSignal<ArtifactListItem | null>(null);
  const [deleting, setDeleting] = createSignal(false);
  const [deleteError, setDeleteError] = createSignal("");
  const [pageCursors, setPageCursors] = createSignal<string[]>([""]);
  const [pageIndex, setPageIndex] = createSignal(0);

  let galleryEl: HTMLDivElement | undefined;
  createEffect(
    on(
      [sourceFilter, chatFilter, () => props.projectId],
      () => {
        setPageCursors([""]);
        setPageIndex(0);
        setLightboxIndex(null);
        if (galleryEl) galleryEl.scrollTop = 0;
      },
      { defer: true },
    ),
  );

  const fetchArgs = () => ({
    projectId: props.projectId.trim(),
    source: sourceFilter(),
    chat: chatFilter(),
    cursor: pageCursors()[pageIndex()] ?? "",
    page: pageIndex(),
  });

  const artifacts = createSurfaceQuery({
    name: "project-artifacts",
    source: () => {
      props.appStore.state.sidecarStatus;
      const c = client();
      const args = fetchArgs();
      return c && args.projectId
        ? { client: c, key: JSON.stringify(args), args }
        : null;
    },
    scope: ({ args }) => args.projectId,
    load: ({ client: c, args }): Promise<ArtifactListResponse> => {
      const query: {
        limit: number;
        cursor?: string;
        source?: Exclude<ArtifactSourceFilter, "all">;
        session_id?: string;
      } = { limit: ARTIFACTS_PAGE_SIZE };
      if (args.cursor) query.cursor = args.cursor;
      if (args.source !== "all") query.source = args.source;
      if (args.chat !== "all") query.session_id = args.chat;
      return c.listProjectArtifacts(args.projectId, query);
    },
    required: false,
  });
  const refetchList = artifacts.refresh;
  const displayedPage = () => artifacts.displayed()?.source.args.page ?? 0;

  createResidentActivity(() =>
    createArtifactRefreshSubscription(
      () => props.projectId,
      () => {
        void refetchList();
      },
    ),
  );

  const items = createMemo(() => artifacts.value()?.artifacts ?? []);
  const hasActiveFilter = () =>
    sourceFilter() !== "all" || chatFilter() !== "all";
  const chatOptions = createMemo(() => {
    const sessions = artifacts.value()?.facets?.session_ids ?? [];
    const active = chatFilter();
    if (active !== "all" && !sessions.includes(active)) {
      return [active, ...sessions];
    }
    return sessions;
  });

  const showPage = (index: number) => {
    setPageIndex(index);
    setLightboxIndex(null);
    if (galleryEl) galleryEl.scrollTop = 0;
  };

  const changePage = (next: number) => {
    if (artifacts.loading()) return;
    if (next < pageIndex()) {
      showPage(next);
      return;
    }
    if (next === pageIndex() + 1) {
      const cursor = artifacts.value()?.next_cursor?.trim();
      if (!cursor) return;
      setPageCursors((held) => {
        const cursors = held.slice(0, pageIndex() + 1);
        cursors[next] = cursor;
        return cursors;
      });
      showPage(next);
    }
  };

  createEffect(() => {
    const response = artifacts.value();
    const shown = displayedPage();
    if (
      !response ||
      artifacts.loading() ||
      shown <= 0 ||
      shown !== pageIndex() ||
      (response.artifacts?.length ?? 0) > 0
    ) {
      return;
    }
    const previous = shown - 1;
    setPageCursors((held) => held.slice(0, previous + 1));
    showPage(previous);
  });

  createEffect(() => {
    const idx = lightboxIndex();
    if (idx == null || artifacts.value() === undefined) return;
    const len = items().length;
    if (len === 0) setLightboxIndex(null);
    else if (idx >= len) setLightboxIndex(len - 1);
  });

  const setTileSrc = (artifactId: string, src: string | null) => {
    setSrcById((prev) => {
      const next = { ...prev };
      if (src) next[artifactId] = src;
      else delete next[artifactId];
      return next;
    });
  };

  const activeItem = () => {
    const idx = lightboxIndex();
    if (idx == null) return null;
    return items()[idx] ?? null;
  };
  const activeSrc = () => {
    const item = activeItem();
    return item ? srcById()[item.id] ?? null : null;
  };
  const canPrev = () => {
    const idx = lightboxIndex();
    return idx != null && idx > 0;
  };
  const canNext = () => {
    const idx = lightboxIndex();
    return idx != null && idx < items().length - 1;
  };
  const goPrev = () => {
    const idx = lightboxIndex();
    if (idx == null || idx <= 0) return;
    setLightboxIndex(idx - 1);
  };
  const goNext = () => {
    const idx = lightboxIndex();
    if (idx == null || idx >= items().length - 1) return;
    setLightboxIndex(idx + 1);
  };

  createEffect(() => {
    if (lightboxIndex() == null) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return;
      if (event.key === "Escape") {
        event.preventDefault();
        setLightboxIndex(null);
        return;
      }
      if (event.key === "ArrowLeft") {
        event.preventDefault();
        goPrev();
        return;
      }
      if (event.key === "ArrowRight") {
        event.preventDefault();
        goNext();
      }
    };
    window.addEventListener("keydown", onKey);
    onCleanup(() => window.removeEventListener("keydown", onKey));
  });

  const confirmDelete = async () => {
    const target = pendingDelete();
    const c = client();
    const projectId = props.projectId.trim();
    if (!target || !c || !projectId || deleting()) return;
    setDeleting(true);
    setDeleteError("");
    try {
      await c.deleteProjectArtifact(projectId, target.id);
      notifyArtifactChanged({
        project_id: projectId,
        artifact_id: target.id,
        op: "deleted",
      });
      setPendingDelete(null);
      setLightboxIndex(null);
      await refetchList();
    } catch (err) {
      setDeleteError(
        err instanceof Error ? err.message : "Could not delete that artifact.",
      );
    } finally {
      setDeleting(false);
    }
  };

  const activeAnchor = () => artifactTranscriptAnchor(activeItem() ?? null);

  const revealActive = () => {
    const anchor = activeAnchor();
    const sessionId = activeItem()?.session_id.trim() ?? "";
    if (!anchor || !sessionId || !props.onRevealInTranscript) return;
    setLightboxIndex(null);
    props.onRevealInTranscript({ sessionId, anchor });
  };

  return (
    <div
      class="project-artifacts-view"
      data-testid="project-artifacts-view"
      aria-busy={artifacts.coldPending() || artifacts.refreshing()}
    >
      <BrowseStagePanel
        appStore={props.appStore}
        back={props.back}
        scrollHost="content"
        contentReady={artifacts.ready}
        title="Artifacts"
        error={
          artifacts.error() !== undefined
            ? artifacts.error()
            : undefined
        }
        errorTestId="project-artifacts-error"
        chips={
          <div
            class="den-artifacts-filters"
            data-testid="project-artifacts-filters"
            data-first-time-tip-anchor="project-artifacts"
          >
            <BrowseSegmented
              options={[...ARTIFACT_SOURCE_OPTIONS]}
              value={sourceFilter()}
              onChange={(id) => setSourceFilter(id as ArtifactSourceFilter)}
              ariaLabel="Artifact source"
              testId="project-artifacts-source-filter"
            />
            <label class="den-artifacts-chat-filter">
              <span data-testid="chats" class="den-artifacts-chat-filter__label">Chats</span>
              <DenSelect
                aria-label="Filter artifacts by chat"
                data-testid="project-artifacts-chat-filter"
                value={chatFilter()}
                options={[
                  { value: "all", label: "All" },
                  ...chatOptions().map((id) => ({
                    value: id,
                    label: shortChatLabel(id),
                  })),
                ]}
                onValueChange={(value) =>
                  setChatFilter(value as ArtifactChatFilter)
                }
              />
            </label>
          </div>
        }
        meta={
          <Show when={artifacts.value() !== undefined && items().length > 0}>
            <span data-testid="project-artifacts-count">
              {items().length} shown
            </span>
          </Show>
        }
      >
        <div class="den-artifacts-stage">
          <Show when={artifacts.showLoading()}>
            <p class="den-artifacts-empty" data-testid="project-artifacts-loading">
              Loading artifacts…
            </p>
          </Show>
          <Show
            when={
              artifacts.value() !== undefined &&
              items().length === 0 &&
              !hasActiveFilter()
            }
          >
            <p class="den-artifacts-empty" data-testid="project-artifacts-empty">
              No artifacts for this project yet.
            </p>
          </Show>
          <Show
            when={
              artifacts.value() !== undefined &&
              items().length === 0 &&
              hasActiveFilter()
            }
          >
            <p
              class="den-artifacts-empty"
              data-testid="project-artifacts-filter-empty"
            >
              No artifacts match these filters.
            </p>
          </Show>
          <Show
            when={
              artifacts.value() !== undefined && items().length > 0
                ? client()
                : null
            }
            keyed
          >
            {(c) => (
              <Scrollport
                class="den-artifacts-grid"
                contentClass="den-artifacts-grid__content"
                viewportRef={(el) => {
                  galleryEl = el;
                }}
                data-testid="project-artifacts-gallery"
                data-count={items().length}
              >
                <KeyedIndex each={items()} keyOf={(a) => a.id}>
                  {(item, index) => (
                    <ArtifactGalleryTile
                      item={item()}
                      index={index()}
                      total={items().length}
                      projectId={props.projectId}
                      client={c}
                      onOpen={setLightboxIndex}
                      onSrc={setTileSrc}
                      onRequestDelete={(target) => {
                        setDeleteError("");
                        setPendingDelete(target);
                      }}
                    />
                  )}
                </KeyedIndex>
              </Scrollport>
            )}
          </Show>
          <Show when={artifacts.value() !== undefined}>
            <TablePager
              page={displayedPage()}
              itemCount={items().length}
              hasNext={Boolean(artifacts.value()?.next_cursor?.trim())}
              loading={artifacts.loading()}
              onPageChange={changePage}
              testId="project-artifacts-pager"
              ariaLabel="Artifact pagination"
            />
          </Show>
        </div>
      </BrowseStagePanel>

      <Show when={lightboxIndex() !== null}>
        <ResidentPortal mount={document.body}>
          <DenOverlay
            data-testid="project-artifacts-lightbox"
            onClick={() => setLightboxIndex(null)}
          >
            <div
              class="den-overlay__panel"
              role="dialog"
              aria-modal="true"
              aria-label={activeItem()?.caption?.trim() || "Visual artifact"}
              onClick={(event) => event.stopPropagation()}
            >
              <header class="den-overlay__header" {...chromeProps()}>
                <span class="den-overlay__title">
                  {activeItem()?.caption?.trim() ||
                    `Visual ${(lightboxIndex() ?? 0) + 1} of ${items().length}`}
                </span>
                <div class="den-overlay__header-actions">
                  <span data-testid="project-artifacts-lightbox-counter">
                    {(lightboxIndex() ?? 0) + 1} / {items().length}
                  </span>
                  <ChromeCloseButton
                    class="den-overlay__close den-inset-icon-btn"
                    label="Close enlarged visual"
                    onClick={() => setLightboxIndex(null)}
                  />
                </div>
              </header>
              <Scrollport
                class="den-overlay__body den-transcript-visual-lightbox__body"
                contentClass="den-transcript-visual-lightbox__content"
              >
                <button
                  type="button"
                  class="den-transcript-visual-lightbox__nav"
                  aria-label="Previous visual"
                  disabled={!canPrev()}
                  onClick={goPrev}
                >
                  ‹
                </button>
                <Switch fallback={
                    <Show when={activeSrc()}>
                      {(url) => (
                        <Show
                          when={isVisualVideoMime(activeItem()?.mime)}
                          fallback={
                            <img
                              src={url()}
                              alt={activeItem()?.caption?.trim() || "Visual artifact"}
                              class="den-transcript-visual-lightbox__img"
                            />
                          }
                        >
                          <video
                            src={url()}
                            class="den-transcript-visual-lightbox__img"
                            controls
                            preload="metadata"
                          />
                        </Show>
                      )}
                    </Show>
                  }>
                  <Match when={isFilmstripMime(activeItem()?.mime) && activeItem()} keyed>
                    {(item) => <TranscriptVisualFilmstrip artifact={item} sessionId={item.session_id} client={client()} enlarged />}
                  </Match>
                  <Match when={isTimelineMime(activeItem()?.mime) && activeItem()} keyed>
                    {(item) => <TranscriptVisualTimeline artifact={item} sessionId={item.session_id} client={client()} />}
                  </Match>
                </Switch>
                <button
                  type="button"
                  class="den-transcript-visual-lightbox__nav"
                  aria-label="Next visual"
                  disabled={!canNext()}
                  onClick={goNext}
                >
                  ›
                </button>
              </Scrollport>
              <footer class="project-artifacts-lightbox__footer">
                <Show when={activeItem()}>
                  {(item) => (
                    <span
                      class="project-artifacts-tile__source"
                      data-testid="project-artifacts-lightbox-source"
                    >
                      {artifactSourceLabel(item().source)}
                    </span>
                  )}
                </Show>
                <Show when={activeAnchor()}>
                  <button
                    type="button"
                    class="project-artifacts-reveal"
                    data-testid="project-artifact-reveal"
                    onClick={revealActive}
                  >
                    Reveal in transcript
                  </button>
                </Show>
              </footer>
            </div>
          </DenOverlay>
        </ResidentPortal>
      </Show>

      <Show when={pendingDelete()} keyed>
        {(target) => (
          <ResidentPortal mount={document.body}>
            <DenOverlay
              data-testid="project-artifact-delete-confirm"
              onClick={() => !deleting() && setPendingDelete(null)}
            >
              <div
                class="den-overlay__panel den-overlay__panel--prompt"
                role="dialog"
                aria-modal="true"
                aria-label="Delete artifact"
                onClick={(event) => event.stopPropagation()}
              >
                <header class="den-overlay__header" {...chromeProps()}>
                  <span class="den-overlay__title">
                    Delete {target.caption?.trim() || "this artifact"}?
                  </span>
                </header>
                <Scrollport class="den-overlay__body">
                  <p
                    class="project-artifacts-delete__impact"
                    data-testid="project-artifact-delete-impact"
                  >
                    {deleteConfirmBody(target)}
                  </p>
                  <Show when={deleteError()}>
                    {(message) => (
                      <p
                        class="project-artifacts-delete__error"
                        role="alert"
                        data-testid="project-artifact-delete-error"
                      >
                        {message()}
                      </p>
                    )}
                  </Show>
                </Scrollport>
                <footer class="project-artifacts-delete__actions">
                  <DenButton
                    variant="secondary"
                    data-testid="project-artifact-delete-cancel"
                    disabled={deleting()}
                    onClick={() => setPendingDelete(null)}
                  >
                    Keep
                  </DenButton>
                  <DenButton
                    variant="danger"
                    data-testid="project-artifact-delete-confirm-action"
                    disabled={deleting()}
                    onClick={() => void confirmDelete()}
                  >
                    {deleting() ? "Deleting…" : "Delete"}
                  </DenButton>
                </footer>
              </div>
            </DenOverlay>
          </ResidentPortal>
        )}
      </Show>
    </div>
  );
}
