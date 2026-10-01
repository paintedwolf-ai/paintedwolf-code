import { contextAction } from "../context-actions.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import {
  Match,
  Show,
  Switch,
  createEffect,
  createMemo,
  createResource,
  createSignal,
  on,
  onCleanup,
  onMount,
} from "solid-js";
import { ResidentPortal } from "../primitives/ResidentPortal.tsx";
import type { LycaonClient } from "../../api/client.ts";
import type { ArtifactListItem, ArtifactListResponse } from "../../api/types.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
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
import { ContextMenu } from "../ContextMenu.tsx";
import { addToChat } from "../../chat/composer/add-to-chat.ts";
import { startChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";
import { isVisualVideoMime } from "../../chat/visual/visual-artifact-model.ts";
import { isFilmstripMime } from "../../chat/visual/filmstrip-zip.ts";
import { artifactPreview } from "../../chat/visual/frame-archive-preview.ts";
import { isTimelineMime } from "../../chat/visual/timeline-archive.ts";
import { TranscriptVisualFilmstrip } from "../transcript/TranscriptVisualFilmstrip.tsx";
import { TranscriptVisualTimeline } from "../transcript/TranscriptVisualTimeline.tsx";
import { artifactTranscriptAnchor } from "../../chat/visual/artifact-transcript-anchor.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import {
  type ArtifactChatFilter,
  type ArtifactSourceFilter,
  artifactSourceLabel,
  filterProjectArtifacts,
  shortChatLabel,
  uniqueArtifactChatIds,
} from "./project-artifacts-filter.ts";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import { notifyArtifactChanged, subscribeArtifactChanges } from "../../chat/visual/artifact-change-store.ts";
import {
  deleteConfirmBody,
  referenceImpactSentence,
} from "../../files/components/artifact-reference-impact.ts";
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

type TileProps = {
  item: ArtifactListItem;
  index: number;
  total: number;
  projectId: string;
  client: LycaonClient;
  onOpen: (index: number) => void;
  onSrc: (artifactId: string, src: string | null) => void;
  onRequestDelete: (item: ArtifactListItem) => void;
};

function ArtifactGalleryTile(props: TileProps) {
  const caption = () => props.item.caption?.trim() || "";
  const artifactId = () => props.item.id.trim();
  const isVideo = () => isVisualVideoMime(props.item.mime);
  let disposed = false;
  let mediaRequest = 0;
  onCleanup(() => { disposed = true; mediaRequest++; });
  const [revealed, setRevealed] = createSignal(false);
  const [menu, setMenu] = createSignal<
    import("../ContextMenu.tsx").ContextMenuAnchor | null
  >(null);
  const [blob] = createResource(
    () => {
      const id = artifactId();
      const sid = props.item.session_id.trim();
      return sid && id ? `${sid}\0${id}` : null;
    },
    async (key) => {
      const request = ++mediaRequest;
      const [sid, id] = key.split("\0");
      if (!sid || !id) throw new Error("artifact fetch missing session or id");
      const body = await props.client.getSessionArtifact(sid, id);
      const preview = await artifactPreview(body);
      if (disposed || request !== mediaRequest) {
        preview.release();
        return null;
      }
      return preview;
    },
  );
  const [src, setSrc] = createSignal<string | null>(null);
  const [decodeFailed, setDecodeFailed] = createSignal(false);
  createEffect(() => {
    const b = blob.error === undefined ? blob() : undefined;
    setDecodeFailed(false);
    setRevealed(false);
    if (!b) {
      setSrc(null);
      setRevealed(false);
      props.onSrc(props.item.id, null);
      return;
    }
    setSrc(b.src);
    props.onSrc(props.item.id, b.src);
    onCleanup(() => b.release());
  });
  const markReady = () => setRevealed(true);
  const createdLabel = () => {
    const ms = Date.parse(props.item.created_at);
    return Number.isFinite(ms) ? formatRelativeTime(ms) : "";
  };

  const onAddToChat = () => {
    const ref = chatRef();
    if (ref) void addToChat(ref);
  };
  const chatRef = () => {
    const projectId = props.projectId.trim();
    const id = artifactId();
    if (!projectId || !id || isVideo()) return null;
    return {
      kind: "artifact" as const,
      projectId,
      artifactId: id,
      name: caption() || id,
      previewUrl: src() ?? undefined,
    };
  };

  return (
    <figure
      class="den-transcript-visual project-artifacts-tile"
      classList={{ "den-transcript-visual--ready": revealed() }}
      data-testid="project-artifact-tile"
      data-artifact-id={artifactId()}
      data-artifact-source={props.item.source}
      draggable={chatRef() ? true : undefined}
      onDragStart={(event) => startChatAttachmentDrag(event, chatRef())}
      onContextMenu={(e) => {
        const projectId = props.projectId.trim();
        if (!projectId || !artifactId()) return;
        e.preventDefault();
        e.stopPropagation();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
    >
      <button
        type="button"
        class="den-transcript-visual__frame"
        aria-label={
          caption()
            ? `Enlarge ${caption()}`
            : `Enlarge visual ${props.index + 1} of ${props.total}`
        }
        aria-haspopup="dialog"
        aria-busy={!revealed() && !blob.error && !decodeFailed()}
        disabled={!src() || !revealed()}
        onClick={() => props.onOpen(props.index)}
      >
        <Show when={src()}>
          {(url) => (
            <Show
              when={isVideo()}
              fallback={
                <img
                  src={url()}
                  alt={caption() || `Visual ${props.index + 1}`}
                  class="den-transcript-visual__img"
                  ref={(el) => {
                    if (el?.complete && el.naturalWidth > 0) markReady();
                  }}
                  onLoad={() => markReady()}
                  onError={() => setDecodeFailed(true)}
                />
              }
            >
              <video
                src={url()}
                class="den-transcript-visual__img"
                muted
                preload="metadata"
                onLoadedData={markReady}
                onError={() => setDecodeFailed(true)}
              />
            </Show>
          )}
        </Show>
      </button>
      <Show when={blob.error || decodeFailed()}>
        <p role="status">This artifact could not be loaded.</p>
      </Show>
      <figcaption class="project-artifacts-tile__meta">
        <Show when={caption()}>
          {(text) => (
            <span class="project-artifacts-tile__caption">{text()}</span>
          )}
        </Show>
        <span
          class="project-artifacts-tile__source"
          data-testid="project-artifact-source-badge"
        >
          {artifactSourceLabel(props.item.source)}
        </span>
        <span class="project-artifacts-tile__chat">
          {shortChatLabel(props.item.session_id)}
        </span>
        <Show when={createdLabel()}>
          {(label) => (
            <span class="project-artifacts-tile__when">{label()}</span>
          )}
        </Show>
      </figcaption>
      <Show when={menu()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            items={[
              {
                label: caption() || "Artifact",
                header: true,
                description:
                  referenceImpactSentence(props.item) || "Nothing refers to it.",
                testId: "menu-artifact-header",
              },
              contextAction("addToChat", {
                testId: "menu-add-to-chat",
                disabled: isVideo(),
                onSelect: onAddToChat,
              }),
              { separator: true },
              contextAction("deleteArtifact", {
                testId: "menu-delete-artifact",

                onSelect: () => props.onRequestDelete(props.item),
              }),
            ]}
            onDismiss={() => setMenu(null)}
          />
        )}
      </Show>
    </figure>
  );
}

export function ProjectArtifactsView(props: Props) {
  onMount(() => requestFirstTimeTip("project-artifacts"));

  const client = () => props.client ?? getLycaonClient() ?? null;
  const [sourceFilter, setSourceFilter] =
    createSignal<ArtifactSourceFilter>("all");
  const [chatFilter, setChatFilter] = createSignal<ArtifactChatFilter>("all");
  const [lightboxIndex, setLightboxIndex] = createSignal<number | null>(null);
  // Tiles keep identity across reorders, so sources are keyed by artifact.
  const [srcById, setSrcById] = createSignal<Record<string, string>>({});

  const [pendingDelete, setPendingDelete] =
    createSignal<ArtifactListItem | null>(null);
  const [deleting, setDeleting] = createSignal(false);
  const [deleteError, setDeleteError] = createSignal("");
  const [pages, setPages] = createSignal<ArtifactListResponse[]>([]);
  const [pageIndex, setPageIndex] = createSignal(0);
  const [loadingPage, setLoadingPage] = createSignal(false);
  const [pageError, setPageError] = createSignal("");
  const artifacts = createSurfaceQuery({
    name: "project-artifacts",
    source: () => { const c = client(); return c ? { client: c, key: props.projectId } : null; },
    load: ({ client, key }) => client.listProjectArtifacts(key, { limit: ARTIFACTS_PAGE_SIZE }),
    required: false,
  });
  const refetchList = artifacts.refresh;
  let pageGeneration = 0;
  onCleanup(() => { pageGeneration++; });
  createEffect(() => {
    const response = artifacts.value();
    if (!response) return;
    pageGeneration++;
    setLoadingPage(false);
    setPages([{ ...response, artifacts: response.artifacts ?? [] }]);
    setPageIndex(0);
    setPageError("");
  });

  createResidentActivity(() => {
    return subscribeArtifactChanges((projectId) => {
      if (projectId === props.projectId.trim()) void refetchList();
    });
  });

  const page = createMemo(() => pages()[pageIndex()] ?? null);
  const items = createMemo(() => page()?.artifacts ?? []);
  const filtered = createMemo(() =>
    filterProjectArtifacts(items(), sourceFilter(), chatFilter()),
  );
  const chatOptions = createMemo(() =>
    uniqueArtifactChatIds(pages().flatMap((entry) => entry.artifacts ?? [])),
  );

  let galleryEl: HTMLDivElement | undefined;
  const showPage = (index: number) => {
    setPageIndex(index);
    setLightboxIndex(null);
    setSrcById({});
    if (galleryEl) galleryEl.scrollTop = 0;
  };

  const goNextPage = async () => {
    const c = client();
    const projectId = props.projectId.trim();
    const index = pageIndex();
    if (index + 1 < pages().length) {
      showPage(index + 1);
      return;
    }
    const cursor = page()?.next_cursor?.trim() ?? "";
    if (!c || !projectId || !cursor || loadingPage()) return;
    const generation = ++pageGeneration;
    const current = () => generation === pageGeneration && client() === c && props.projectId.trim() === projectId;
    setLoadingPage(true);
    setPageError("");
    try {
      const response = await c.listProjectArtifacts(projectId, {
        limit: ARTIFACTS_PAGE_SIZE,
        cursor,
      });
      if (!current()) return;
      setPages((current) => [
        ...current,
        { ...response, artifacts: response.artifacts ?? [] },
      ]);
      showPage(index + 1);
    } catch (error) {
      if (current()) setPageError(error instanceof Error ? error.message : "Could not load the next artifact page.");
    } finally {
      if (current()) setLoadingPage(false);
    }
  };

  const changePage = (next: number) => {
    if (next < pageIndex()) {
      showPage(next);
      return;
    }
    if (next === pageIndex() + 1) void goNextPage();
  };

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
    return filtered()[idx] ?? null;
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
    return idx != null && idx < filtered().length - 1;
  };
  const goPrev = () => {
    const idx = lightboxIndex();
    if (idx == null || idx <= 0) return;
    setLightboxIndex(idx - 1);
  };
  const goNext = () => {
    const idx = lightboxIndex();
    if (idx == null || idx >= filtered().length - 1) return;
    setLightboxIndex(idx + 1);
  };

  createEffect(
    on([sourceFilter, chatFilter], () => {
      setPageIndex(0);
      setLightboxIndex(null);
      setSrcById({});
      if (galleryEl) galleryEl.scrollTop = 0;
    }, { defer: true }),
  );

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
      notifyArtifactChanged({ project_id: projectId, artifact_id: target.id, op: "deleted" });
      setPendingDelete(null);
      setLightboxIndex(null);
      setSrcById({});
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
      aria-busy={
        artifacts.coldPending() ||
        artifacts.refreshing() ||
        loadingPage()
      }
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
              <span class="den-artifacts-chat-filter__label">Chats</span>
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
          <Show when={(artifacts.value() !== undefined) && items().length > 0}>
            <span data-testid="project-artifacts-count">
              {filtered().length} shown
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
          <Show when={(artifacts.value() !== undefined) && items().length === 0}>
            <p class="den-artifacts-empty" data-testid="project-artifacts-empty">
              No artifacts for this project yet.
            </p>
          </Show>
          <Show
            when={
              (artifacts.value() !== undefined) &&
              items().length > 0 &&
              filtered().length === 0
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
              (artifacts.value() !== undefined) && filtered().length > 0
                ? client()
                : null
            }
            keyed
          >
            {(c) => (
              <Scrollport
                class="den-artifacts-grid"
                contentClass="den-artifacts-grid__content"
                viewportRef={(el) => { galleryEl = el; }}
                data-testid="project-artifacts-gallery"
                data-count={filtered().length}
              >
                <KeyedIndex each={filtered()} keyOf={(a) => a.id}>
                  {(item, index) => (
                    <ArtifactGalleryTile
                      item={item()}
                      index={index()}
                      total={filtered().length}
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
          <Show when={(artifacts.value() !== undefined)}>
            <TablePager
              page={pageIndex()}
              itemCount={filtered().length}
              hasNext={Boolean(page()?.next_cursor?.trim())}
              loading={loadingPage()}
              onPageChange={changePage}
              testId="project-artifacts-pager"
              ariaLabel="Artifact pagination"
            />
          </Show>
          <Show when={pageError()}>
            {(message) => (
              <p
                class="den-artifacts-page-error"
                data-testid="project-artifacts-page-error"
              >
                {message()}
              </p>
            )}
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
                    `Visual ${(lightboxIndex() ?? 0) + 1} of ${filtered().length}`}
                </span>
                <div class="den-overlay__header-actions">
                  <span data-testid="project-artifacts-lightbox-counter">
                    {(lightboxIndex() ?? 0) + 1} / {filtered().length}
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
