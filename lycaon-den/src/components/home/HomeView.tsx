import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { noticeReporterFor } from "../../platform/connection/app-connection.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import { PreflightNudge } from "./PreflightNudge.tsx";
import { WhatsNewCard } from "./WhatsNewCard.tsx";
import { AttentionBand } from "./AttentionBand.tsx";
import type { AttentionRow } from "../../api/types.ts";
import type { ProjectSummary } from "../../project/project-summary.ts";
import {
  ALL_PROJECTS_PAGE_SIZE,
  HOME_GRID_CAP,
  type HomeSection,
  type ProjectListSortDir,
  type ProjectListSortKey,
  nextProjectListSortDir,
  projectsForSection,
  sectionEmptyHint,
  sectionTitle,
  sortProjectSummaries,
} from "../../home/home-model.ts";
import { paginateSlice } from "../../list/pagination.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { type LoadState, errorOf, isLoaded, isResolved } from "../../store/load-state.ts";
import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";
import { NoProviderCard } from "../NoProviderCard.tsx";
import {
  NO_PROVIDER_PREFLIGHT_CODE,
  type ProviderGap,
} from "../../notices/no-provider-card.ts";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { surfaceRevealDom, useSurfaceReveal } from "../../ui/surface-reveal.ts";
import { TablePager } from "../list/TablePager.tsx";
import { ProjectCard } from "./ProjectCard.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { ProjectListRow } from "./ProjectListRow.tsx";
import { BrowseChrome } from "../browse/BrowseChrome.tsx";
import { BrowseOverflowMenu } from "../browse/BrowseOverflowMenu.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { registerChatDrop, type DroppedItem } from "../../platform/files/file-drop.ts";
import {
  ATTACHMENT_PROVIDER_SEND_HINT,
  filesFromClipboard,
} from "../../chat/composer/composer-attachments.ts";
import {
  HOME_IDEA_DROP_AFFORDANCE,
  homeIdeaChipDetail,
  homeIdeaChipGlyph,
  homeIdeaRejectsPresent,
  intakeHomeIdeaDrop,
  revokeHomeIdeaAttachment,
  type HomeIdeaAttachment,
  type HomeIdeaDraft,
} from "../../home/idea-attachments.ts";

type Props = {
  summaries: ProjectSummary[];
  section: HomeSection;
  /** Registry read lifecycle; empty-state and teaching copy require a settled load. */
  registry: LoadState<null>;
  /** Which half of the model config is missing, when either is. */
  providerGap?: ProviderGap;
  onOpenProviders?: () => void;
  onOpenDiagnostics?: () => void;
  /** The submitted Home draft is becoming its first durable chat. */
  submitting?: boolean;
  onSubmitIdea: (draft: HomeIdeaDraft) => boolean | Promise<boolean>;
  onOpenFolder: () => void;
  onCloneRepo: () => void;
  onOpenProject: (id: string) => void;
  onToggleStar: (id: string, starred: boolean) => void;
  onRename: (id: string, name: string) => void;
  onAttachFolder: (id: string) => void;
  onPromote: (id: string) => void;
  onDelete: (id: string, opts?: { confirmed?: boolean }) => void;
  /** Blocked chats across every project; empty renders nothing. */
  attentionRows?: readonly AttentionRow[];
  onOpenAttention?: (row: AttentionRow) => void;
  onShowAllProjects?: () => void;
};

/** The idea field grows inside its scroll host, which caps it at about five lines. */
const MIN_IDEA_HEIGHT_PX = 21; // one line at 14px / 1.45

function autoGrowIdea(el: HTMLTextAreaElement | undefined): void {
  if (!el) return;
  // Wait for a usable stage width before measuring.
  if (el.clientWidth === 0) return;
  el.style.height = "auto";
  el.style.height = `${Math.max(el.scrollHeight, MIN_IDEA_HEIGHT_PX)}px`;
  el.style.overflowY = "hidden";
}

function confirmBulkDelete(count: number): Promise<boolean> {
  return confirmDestructive({
    message: `Delete ${count} projects? This cannot be undone.`,
    title: "Delete projects",
    okLabel: `Delete ${count}`,
  });
}

function sortGlyph(active: boolean, direction: ProjectListSortDir): string {
  if (!active) return "";
  return direction === "asc" ? "↑" : "↓";
}

export function HomeView(props: Props) {
  const [idea, setIdea] = createSignal("");
  const [ideaAttachments, setIdeaAttachments] = createSignal<HomeIdeaAttachment[]>([]);
  const [dragActive, setDragActive] = createSignal(false);
  const [listPage, setListPage] = createSignal(0);
  const [listSortKey, setListSortKey] = createSignal<ProjectListSortKey>("updated");
  const [listSortDir, setListSortDir] = createSignal<ProjectListSortDir>("desc");
  const [selected, setSelected] = createSignal<Set<string>>(new Set<string>());
  const [providerDismissed, setProviderDismissed] = createSignal(false);
  let ideaRef: HTMLTextAreaElement | undefined;
  let selectAllRef: HTMLInputElement | undefined;
  let frameRef: HTMLElement | undefined;
  const summaryById = createMemo(() => new Map(props.summaries.map((project) => [project.id, project])));
  const rows = () => projectsForSection(props.summaries, props.section);
  // First-run guidance requires a settled empty registry.
  const teaching = () =>
    isLoaded(props.registry) &&
    rows().length === 0 &&
    (props.section === "recents" || props.section === "all");
  const registryFailed = () => errorOf(props.registry) !== undefined;
  const sortedAllRows = () =>
    sortProjectSummaries(rows(), listSortKey(), listSortDir());
  const allPaged = () =>
    paginateSlice(sortedAllRows(), listPage(), ALL_PROJECTS_PAGE_SIZE);
  const selectedCount = () => selected().size;
  const pageIds = () => allPaged().slice.map((p) => p.id);
  const pageSelectedCount = () => pageIds().filter((id) => selected().has(id)).length;
  const pageAllSelected = () => {
    const ids = pageIds();
    return ids.length > 0 && ids.every((id) => selected().has(id));
  };

  const toggleListSort = (key: ProjectListSortKey) => {
    setListSortDir(nextProjectListSortDir(listSortKey(), key, listSortDir()));
    setListSortKey(key);
    setListPage(0);
  };

  createEffect(() => {
    if (props.section !== "all") {
      setListPage(0);
      setSelected(new Set<string>());
      return;
    }
    const pageCount = Math.max(1, Math.ceil(rows().length / ALL_PROJECTS_PAGE_SIZE));
    if (listPage() > pageCount - 1) setListPage(Math.max(0, pageCount - 1));
    const alive = new Set(rows().map((r) => r.id));
    setSelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      for (const id of prev) {
        if (alive.has(id)) next.add(id);
        else changed = true;
      }
      return changed ? next : prev;
    });
  });

  createEffect(() => {
    const el = selectAllRef;
    if (!el) return;
    const some = pageSelectedCount() > 0;
    el.indeterminate = some && !pageAllSelected();
  });

  const boot = useSurfaceReveal({
    ready: () => isResolved(props.registry),
  });
  const bootAttrs = () => surfaceRevealDom(boot);

  createEffect(() => {
    idea();
    // Re-measure after the stage becomes visible.
    boot.ready();
    autoGrowIdea(ideaRef);
  });

  // Serialized so a burst of drops sees the caps it actually lands against.
  let intakeQueue: Promise<void> = Promise.resolve();
  const appendDroppedItems = (items: readonly DroppedItem[]) => {
    if (props.submitting || items.length === 0) return;
    intakeQueue = intakeQueue.then(async () => {
      const added = await intakeHomeIdeaDrop(items, ideaAttachments());
      if (added.length > 0) setIdeaAttachments((prev) => [...prev, ...added]);
    });
  };

  const removeIdeaAttachment = (id: string) => {
    setIdeaAttachments((prev) => {
      const victim = prev.find((item) => item.id === id);
      if (victim) revokeHomeIdeaAttachment(victim);
      return prev.filter((item) => item.id !== id);
    });
  };

  const clearIdeaAttachments = () => {
    for (const item of ideaAttachments()) revokeHomeIdeaAttachment(item);
    setIdeaAttachments([]);
  };

  onMount(() => {
    const detachDrop = registerChatDrop({
      htmlTarget: frameRef,
      onDragActive: (active) => setDragActive(active && !props.submitting),
      onDrop: (event) => {
        setDragActive(false);
        appendDroppedItems(event.items);
      },
    });
    onCleanup(detachDrop);
    onCleanup(() => {
      for (const item of ideaAttachments()) revokeHomeIdeaAttachment(item);
    });
  });

  const rejectsBlockSubmit = () => homeIdeaRejectsPresent(ideaAttachments());
  const sendableIdeaAttachments = () =>
    ideaAttachments().filter((item) => item.kind !== "reject");
  const canSubmit = () =>
    !props.submitting &&
    !rejectsBlockSubmit() &&
    (idea().trim().length > 0 || sendableIdeaAttachments().length > 0);

  const submit = async () => {
    if (!canSubmit()) return;
    const text = idea();
    const attachments = ideaAttachments();
    try {
      const accepted = await props.onSubmitIdea({ text: text.trim(), attachments });
      if (!accepted) return;
      // Preserve edits made while submission was pending.
      if (idea() === text) setIdea("");
      if (ideaAttachments() === attachments) clearIdeaAttachments();
    } catch (cause) {
      noticeReporterFor(APP_SCOPE).reportError(cause);
    }
  };

  const deleteOne = (id: string) => {
    props.onDelete(id);
  };

  const toggleSelect = (id: string, on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const toggleSelectPage = () => {
    const ids = pageIds();
    setSelected((prev) => {
      const next = new Set(prev);
      if (pageAllSelected()) {
        for (const id of ids) next.delete(id);
      } else {
        for (const id of ids) next.add(id);
      }
      return next;
    });
  };

  const selectAllProjects = () => {
    setSelected(new Set(rows().map((r) => r.id)));
  };

  const clearSelection = () => setSelected(new Set<string>());

  const bulkStar = (starred: boolean) => {
    for (const id of selected()) props.onToggleStar(id, starred);
  };

  const bulkDelete = async () => {
    const ids = [...selected()];
    const firstId = ids[0];
    if (ids.length === 1 && firstId !== undefined) {
      props.onDelete(firstId);
      clearSelection();
      return;
    }
    if (ids.length === 0) return;
    if (!(await confirmBulkDelete(ids.length))) return;
    for (const id of ids) props.onDelete(id, { confirmed: true });
    clearSelection();
  };

  return (
    <section
      ref={frameRef}
      class="home-view-frame"
      classList={bootAttrs().classList}
      data-boot={bootAttrs()["data-boot"]}
      data-testid="home-view"
      aria-busy={props.submitting || bootAttrs()["aria-busy"]}
      aria-hidden={bootAttrs()["aria-hidden"]}
      inert={bootAttrs().inert}
    >
      <Show when={dragActive()}>
        <div
          class="den-chat-drop-overlay"
          data-testid="home-drop-overlay"
          aria-hidden="true"
        >
          <p class="den-chat-drop-overlay__affordance">
            {HOME_IDEA_DROP_AFFORDANCE}
          </p>
        </div>
      </Show>
      <Scrollport class="home-view" contentClass="home-view__content">
        <ChromeDragSurface class="home-view__chrome-drag" />
        {/* Contextual cards share one Home column. */}
        <div class="home-view__notices" data-testid="home-notices">
          <WhatsNewCard />
          <Show when={!providerDismissed() ? props.providerGap : undefined} keyed>
            {(gap) => (
              <NoProviderCard
                gap={gap}
                onOpenProviders={() => props.onOpenProviders?.()}
                onDismiss={() => setProviderDismissed(true)}
              />
            )}
          </Show>
          {/* The provider card controls its readiness code. */}
          <PreflightNudge
            statedElsewhere={
              props.providerGap ? [NO_PROVIDER_PREFLIGHT_CODE] : []
            }
            onOpenDiagnostics={
              props.onOpenDiagnostics
                ? () => props.onOpenDiagnostics?.()
                : undefined
            }
          />
        </div>
        {/* Attention precedes new work. */}
        <AttentionBand
          rows={props.attentionRows ?? []}
          onOpen={(row) => props.onOpenAttention?.(row)}
        />
        <div class="home-view__idea">
          <div class="home-view__idea-row">
            <span class="home-view__idea-icon" aria-hidden="true">
              <ThemeIcon slot="sparkle" size={17} />
            </span>
            <Scrollport class="home-view__idea-scroll" eager>
              <textarea
                ref={ideaRef}
                class="home-view__idea-input"
                data-testid="home-idea-input"
                rows={1}
                placeholder="Describe your idea…"
                value={idea()}
                disabled={props.submitting}
                onInput={(e) => {
                  setIdea(e.currentTarget.value);
                  autoGrowIdea(e.currentTarget);
                }}
                onPaste={(e) => {
                  const files = filesFromClipboard(e.clipboardData?.items);
                  if (files.length === 0) return;
                  e.preventDefault();
                  appendDroppedItems(
                    files.map((file) => ({ source: "file" as const, file })),
                  );
                }}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    void submit();
                  }
                }}
              />
            </Scrollport>
            <button
              type="button"
              class="home-view__idea-send"
              data-testid="home-idea-send"
              disabled={!canSubmit()}
              aria-label={props.submitting ? "Starting chat" : "Start"}
              onClick={() => void submit()}
            >
              <ThemeIcon slot={props.submitting ? "pending" : "upload"} size={16} />
            </button>
            <Show when={props.submitting}>
              <span class="sr-only" role="status">Starting chat…</span>
            </Show>
          </div>
          <Show when={ideaAttachments().length > 0}>
            <div class="den-composer-attach-note">
              <span data-testid="home-idea-attach-count">
                {ideaAttachments().length === 1
                  ? "1 attached"
                  : `${ideaAttachments().length} attached`}
              </span>
              <span class="den-composer-attach-note-hint">
                {ATTACHMENT_PROVIDER_SEND_HINT}
              </span>
              <button
                type="button"
                class="den-composer-attach-clear"
                data-testid="home-idea-attach-clear"
                onClick={clearIdeaAttachments}
              >
                Clear all
              </button>
            </div>
            <Scrollport
              class="den-composer-chip-rail"
              contentAs="ul"
              contentClass="den-composer-chip-list"
              content={{ "data-testid": "home-idea-attachment-chips" }}
            >
              <For each={ideaAttachments()}>
                {(att) => (
                  <li
                    class="den-composer-chip"
                    classList={{ "den-composer-chip--reject": att.kind === "reject" }}
                    data-testid="home-idea-attachment-chip"
                    data-kind={att.kind}
                    data-tip={
                      homeIdeaChipDetail(att)
                        ? `${att.name} · ${homeIdeaChipDetail(att)}`
                        : att.name
                    }
                    data-tip-when-clipped=".den-composer-chip-label, .den-composer-chip-detail"
                  >
                    <Show
                      when={
                        att.kind === "web-file" && att.previewUrl
                          ? att.previewUrl
                          : undefined
                      }
                      keyed
                      fallback={
                        <span class="den-composer-chip-glyph" aria-hidden="true">
                          {homeIdeaChipGlyph(att)}
                        </span>
                      }
                    >
                      {(url) => (
                        <img
                          class="den-composer-chip-thumb"
                          src={url}
                          alt={att.name}
                        />
                      )}
                    </Show>
                    <span class="den-composer-chip-label">{att.name}</span>
                    <Show when={homeIdeaChipDetail(att)}>
                      <span class="den-composer-chip-detail">
                        {homeIdeaChipDetail(att)}
                      </span>
                    </Show>
                    <button
                      type="button"
                      class="den-composer-chip-remove"
                      data-testid="home-idea-attachment-remove"
                      aria-label={`Remove ${att.name}`}
                      onClick={() => removeIdeaAttachment(att.id)}
                    >
                      ×
                    </button>
                  </li>
                )}
              </For>
            </Scrollport>
          </Show>
          <Show when={rejectsBlockSubmit()}>
            <p class="home-view__idea-error" data-testid="home-idea-attach-error">
              Remove unsupported attachments before starting.
            </p>
          </Show>
        </div>

        <div class="home-view__doors" data-testid="home-doors">
          <div class="home-view__doors-divider">
            <span class="home-view__doors-line" aria-hidden="true" />
            <span class="home-view__doors-label">or work with code you already have</span>
            <span class="home-view__doors-line" aria-hidden="true" />
          </div>
          <div class="home-view__doors-actions">
            <button
              type="button"
              class="home-view__door"
              data-testid="home-open-folder"
              onClick={() => props.onOpenFolder()}
            >
              <ThemeIcon slot="open-folder" size={15} />
              Open a folder…
            </button>
            <button
              type="button"
              class="home-view__door"
              data-testid="home-clone-repo"
              onClick={() => props.onCloneRepo()}
            >
              <ThemeIcon slot="clone-repo" size={15} />
              Clone a repo…
            </button>
          </div>
        </div>

        <Show when={!teaching()}>
          <div class="home-view__section-head" {...chromeProps()}>
            <span class="home-view__section-title">{sectionTitle(props.section)}</span>
            <Show when={rows().length > 0}>
              <span class="home-view__section-count">{rows().length}</span>
            </Show>
          </div>
        </Show>

        <Show
          when={rows().length > 0}
          fallback={
            <div class="home-view__empty-pane" data-testid="home-empty-pane">
              <Show
                when={teaching()}
                fallback={
                  <p class="home-view__empty" data-testid="home-empty">
                    {registryFailed()
                      ? "Couldn’t load your projects. They’re still there — retry from the sidebar or restart the app."
                      : isLoaded(props.registry)
                        ? sectionEmptyHint(props.section)
                        : "Loading projects…"}
                  </p>
                }
              >
                <TeachingCTA />
              </Show>
            </div>
          }
        >
          <Show
            when={props.section === "all"}
            fallback={
              <div class="home-view__grid" data-testid="home-grid">
                <For each={rows().slice(0, HOME_GRID_CAP).map((project) => project.id)}>
                  {(id) => (
                    <ShowLatest when={summaryById().get(id)}>
                      {(project) => (
                        <ProjectCard
                          project={project()}
                          onOpen={(id) => props.onOpenProject(id)}
                          onToggleStar={(id, s) => props.onToggleStar(id, s)}
                          onRename={(id, n) => props.onRename(id, n)}
                          onAttachFolder={(id) => props.onAttachFolder(id)}
                          onPromote={(id) => props.onPromote(id)}
                          onDelete={(id) => void deleteOne(id)}
                        />
                      )}
                    </ShowLatest>
                  )}
                </For>
                <Show when={rows().length > HOME_GRID_CAP}>
                  <button
                    type="button"
                    class="home-grid-overflow"
                    data-testid="home-grid-overflow"
                    onClick={() => props.onShowAllProjects?.()}
                  >
                    Show all {rows().length} projects
                  </button>
                </Show>
              </div>
            }
          >
            <div class="den-list-panel" data-testid="home-list">
              <div class="den-list-head">
                <label class="project-list-check">
                  <DenCheckboxControl
                    ref={selectAllRef}
                    checked={pageAllSelected()}
                    aria-label="Select all on this page"
                    data-testid="home-list-select-page"
                    onChange={toggleSelectPage}
                  />
                </label>
                <span class="den-list-head-glyph" aria-hidden="true" />
                <button
                  type="button"
                  class="den-list-sort"
                  classList={{ "den-list-sort--active": listSortKey() === "name" }}
                  aria-sort={
                    listSortKey() === "name"
                      ? listSortDir() === "asc"
                        ? "ascending"
                        : "descending"
                      : "none"
                  }
                  data-testid="home-list-sort-name"
                  onClick={() => toggleListSort("name")}
                >
                  Name
                  <span class="den-list-sort-glyph" aria-hidden="true">
                    {sortGlyph(listSortKey() === "name", listSortDir())}
                  </span>
                </button>
                <button
                  type="button"
                  class="den-list-sort den-list-sort--activity"
                  classList={{ "den-list-sort--active": listSortKey() === "updated" }}
                  aria-sort={
                    listSortKey() === "updated"
                      ? listSortDir() === "asc"
                        ? "ascending"
                        : "descending"
                      : "none"
                  }
                  data-testid="home-list-sort-updated"
                  onClick={() => toggleListSort("updated")}
                >
                  Updated
                  <span class="den-list-sort-glyph" aria-hidden="true">
                    {sortGlyph(listSortKey() === "updated", listSortDir())}
                  </span>
                </button>
                <span class="den-list-head-actions" aria-hidden="true" />
              </div>
              <Show when={selectedCount() > 0}>
                <BrowseChrome
                  variant="embed"
                  testId="home-list-toolbar"
                  overflow={
                    <BrowseOverflowMenu
                      testId="home-list-overflow"
                      items={[
                        {
                          label: "Delete",
                          danger: true,
                          testId: "home-list-bulk-delete",
                          onSelect: () => void bulkDelete(),
                        },
                      ]}
                    />
                  }
                  chips={
                    <>
                      <Show when={selectedCount() < rows().length}>
                        <DenButton
                          variant="ghost"
                          compact
                          data-testid="home-list-select-all"
                          onClick={selectAllProjects}
                        >
                          Select all {rows().length}
                        </DenButton>
                      </Show>
                      <DenButton
                        variant="ghost"
                        compact
                        data-testid="home-list-bulk-star"
                        onClick={() => bulkStar(true)}
                      >
                        Star
                      </DenButton>
                      <DenButton
                        variant="ghost"
                        compact
                        data-testid="home-list-bulk-unstar"
                        onClick={() => bulkStar(false)}
                      >
                        Unstar
                      </DenButton>
                      <DenButton
                        variant="ghost"
                        compact
                        data-testid="home-list-clear-selection"
                        onClick={clearSelection}
                      >
                        Clear
                      </DenButton>
                    </>
                  }
                  meta={
                    <span>
                      {selectedCount()} selected
                    </span>
                  }
                />
              </Show>
              <ul class="den-list">
                <For each={allPaged().slice.map((project) => project.id)}>
                  {(id) => (
                    <ShowLatest when={summaryById().get(id)}>
                      {(project) => (
                        <ProjectListRow
                          project={project()}
                          selected={selected().has(id)}
                          onToggleSelect={toggleSelect}
                          onOpen={(id) => props.onOpenProject(id)}
                          onToggleStar={(id, s) => props.onToggleStar(id, s)}
                          onRename={(id, n) => props.onRename(id, n)}
                          onAttachFolder={(id) => props.onAttachFolder(id)}
                          onPromote={(id) => props.onPromote(id)}
                          onDelete={(id) => void deleteOne(id)}
                        />
                      )}
                    </ShowLatest>
                  )}
                </For>
              </ul>
              <TablePager
                page={listPage()}
                pageSize={ALL_PROJECTS_PAGE_SIZE}
                total={allPaged().total}
                onPageChange={setListPage}
                testId="home-list-pager"
              />
            </div>
          </Show>
        </Show>
      </Scrollport>
    </section>
  );
}

function TeachingCTA() {
  const steps = [
    {
      title: "Describe it",
      body: "Tell us what you want to build or change.",
    },
    {
      title: "Painted Wolf builds",
      body: "It scaffolds, edits, and runs tests while you steer.",
    },
    {
      title: "Save as a project",
      body: "Like where it's going? Save to a folder and pick up anytime.",
    },
  ];
  return (
    <div class="home-view__teach" data-testid="home-teaching">
      <div class="home-view__teach-intro">
        <h2 class="home-view__teach-heading">Hello!</h2>
        <p class="home-view__teach-lede">
          Start with an idea or open a folder.
        </p>
      </div>
      <ol class="home-view__teach-list">
        <For each={steps}>
          {(s, i) => (
            <li class="home-view__teach-step">
              <span class="home-view__teach-num" aria-hidden="true">
                {i() + 1}
              </span>
              <div class="home-view__teach-copy">
                <p class="home-view__teach-title">{s.title}</p>
                <p class="home-view__teach-body">{s.body}</p>
              </div>
            </li>
          )}
        </For>
      </ol>
    </div>
  );
}
