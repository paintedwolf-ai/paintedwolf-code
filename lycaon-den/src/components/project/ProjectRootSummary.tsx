import {
  For,
  Show,
  createMemo,
  createEffect,
  createSignal,
  onCleanup,
  onMount,
  untrack,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  GitRepoEntry,
  ProjectAgentContext,
  ProjectRoot,
  SourceIndexResource,
} from "../../api/types.ts";
import type { GitWorkspaceStatus } from "../../chat/actions/git-workspace-status.ts";
import { OpenInButton } from "../OpenInButton.tsx";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { formatSourceMtime } from "../source/editor/source-editor-model.ts";
import type { FilesTreeSelection } from "../../files/tree/files-tree-context.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { PreparedSurface } from "../primitives/PreparedSurface.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { createRetainedMemo } from "../../store/projection-store.ts";
import { createPresentationWaiting } from "../../ui/presentation.ts";
import { observeWorkspaceInvalidation } from "../../files/source/workspace-invalidation.ts";
import { subscribeSourceInvalidation } from "../../files/source/source-invalidation.ts";
import { SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS } from "../../files/source/source-refresh.ts";
import { createAdaptivePoller } from "../../store/adaptive-poller.ts";
import { createBoundedDebouncedAsyncScheduler } from "../../store/coalesced-async.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { sourceIndexNotes } from "../../search/search-status.ts";

type Props = {
  projectId: string;
  /** Addresses the checkout when a fact is read; a new chat alone does not reload one. */
  sessionId?: string;
  root: ProjectRoot;
  roots: ProjectRoot[];
  client?: LycaonClient | null;
  onOpenFile: (selection: FilesTreeSelection) => void;
  onOpenChanges: () => void;
};

type GitSummary = Pick<
  GitWorkspaceStatus,
  | "available"
  | "branch"
  | "head_short"
  | "upstream"
  | "ahead"
  | "behind"
  | "dirty"
  | "staged_count"
  | "unstaged_count"
  | "changed_count"
  | "files"
> & { statusPending: boolean };

type Resource = {
  label: string;
  description: string;
  path: string;
};

type InventorySummary = {
  count: number | null;
  resources: Resource[];
  warming: boolean;
  note: string;
};

type RootSummarySource = {
  client: LycaonClient;
  key: string;
  projectId: string;
  sessionId?: string;
  root: ProjectRoot;
};

type RootSummary = {
  git: GitSummary | null;
  inventory: InventorySummary | null;
  agentContext: ProjectAgentContext | null;
};

type RootFact = keyof RootSummary;

const ALL_FACTS: ReadonlySet<RootFact> = new Set(["git", "inventory", "agentContext"]);

function gitSummaryFromRepo(repo: GitRepoEntry): GitSummary {
  return {
    available: repo.available,
    statusPending: repo.status_pending ?? false,
    branch: repo.branch,
    head_short: repo.head_short,
    upstream: repo.upstream,
    ahead: repo.ahead,
    behind: repo.behind,
    dirty: repo.dirty,
    staged_count: repo.staged_count,
    unstaged_count: repo.unstaged_count,
    changed_count: repo.changed_count,
    files: [],
  };
}

function indexedResource(resource: SourceIndexResource): Resource {
  switch (resource.kind) {
    case "agents":
      return { label: "AGENTS.md", description: "Agent instructions", path: resource.path };
    case "contributing":
      return { label: "CONTRIBUTING", description: "Contribution guide", path: resource.path };
    default:
      return { label: "README", description: "Project overview", path: resource.path };
  }
}

function changeCount(git: GitSummary | null): number {
  if (!git?.available) return 0;
  return git.changed_count ?? git.staged_count + git.unstaged_count;
}

function changedLabel(git: GitSummary | null): string {
  const count = changeCount(git);
  return count === 0 ? "Clean" : `${count} changed`;
}

function syncLabel(git: GitSummary | null, loading: boolean): string {
  if (loading || git?.statusPending) return "Checking…";
  if (!git?.available) return "—";
  if (!(git.upstream ?? "").trim()) return "No upstream";
  const parts: string[] = [];
  if (git.ahead > 0) parts.push(`${git.ahead} ahead`);
  if (git.behind > 0) parts.push(`${git.behind} behind`);
  return parts.length > 0 ? parts.join(" · ") : "Up to date";
}

/** Targeted refreshes retain unrelated facts from the same source. */
async function loadRootSummary(
  source: RootSummarySource,
  signal: AbortSignal,
  facts: ReadonlySet<RootFact>,
  previous: RootSummary | undefined,
): Promise<RootSummary> {
  const { client, projectId, sessionId, root } = source;
  const read = <K extends RootFact>(fact: K, load: () => Promise<RootSummary[K]>): Promise<RootSummary[K]> =>
    previous && !facts.has(fact) ? Promise.resolve(previous[fact]) : load();
  // Independent requests preserve each other's results on failure.
  const git = read("git", async () => {
    const repos = await client.listGitRepos(projectId, sessionId);
    const repo = repos.repos.find((entry) => entry.root_ids.includes(root.id));
    return repo ? gitSummaryFromRepo(repo) : null;
  });
  const inventory = read("inventory", async () => {
    const summary = await client.getProjectSourceIndex(projectId, { sessionId });
    const coverage = summary.coverage.find(entry => entry.root_id === root.id);
    const indexedRoot = summary.roots.find((entry) => entry.root_id === root.id);
    return {
      count: indexedRoot?.file_count ?? null,
      resources: (indexedRoot?.resources ?? []).map(indexedResource),
      warming: !!coverage && !coverage.error && coverage.state !== "failed" && (!coverage.discovery_complete || coverage.refreshing),
      note: coverage ? sourceIndexNotes([coverage]) : "The file index is unavailable. Try again.",
    };
  });
  const agentContext = read("agentContext", async () =>
    client.getProjectAgentContext(projectId, root.id, { sessionId }));
  const [gitResult, inventoryResult, contextResult] = await Promise.allSettled([
    git,
    inventory,
    agentContext,
  ]);
  if (signal.aborted) throw signal.reason;
  // A failed refresh keeps the fact it would have replaced.
  const settled = <T,>(result: PromiseSettledResult<T>, held: T | undefined): T | null =>
    result.status === "fulfilled" ? result.value : held ?? null;
  return {
    git: settled(gitResult, previous?.git),
    inventory: inventoryResult.status === "fulfilled" ? inventoryResult.value : {
      count: previous?.inventory?.count ?? null,
      resources: previous?.inventory?.resources ?? [],
      warming: false,
      note: "The file index could not be refreshed. Try again.",
    },
    agentContext: settled(contextResult, previous?.agentContext),
  };
}

export function ProjectRootSummary(props: Props) {
  onMount(() => {
    requestFirstTimeTip("files-project-roots");
  });
  const [copied, setCopied] = createSignal(false);
  const [revision, setRevision] = createSignal(0);
  createEffect(() => {
    const client = props.client;
    if (!client) return;
    onCleanup(observeWorkspaceInvalidation(client, props.projectId, () => props.sessionId, () => {
      setRevision((value) => value + 1);
    }));
  });
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;

  onCleanup(() => {
    if (copiedTimer) clearTimeout(copiedTimer);
  });
  const source = (): RootSummarySource | null => {
    const client = props.client;
    if (!client) return null;
    return {
      client,
      key: JSON.stringify([
        props.projectId,
        props.root.id,
        props.root.path,
        revision(),
      ]),
      projectId: props.projectId,
      sessionId: props.sessionId,
      root: props.root,
    };
  };
  // Targeted refreshes name their facts; activation and new sources read every fact.
  let requestedFacts: Set<RootFact> | null = null;
  const query = createSurfaceQuery({
    name: "project-root-summary",
    source,
    scope: (current) => current.projectId,
    load: async (current: RootSummarySource, signal: AbortSignal): Promise<RootSummary> => {
      const facts = requestedFacts ?? ALL_FACTS;
      requestedFacts = null;
      const shown: { source: RootSummarySource; value: RootSummary } | undefined = untrack(query.displayed);
      const previous = shown?.source.key === current.key ? shown.value : undefined;
      try {
        return await loadRootSummary(current, signal, facts, previous);
      } catch (error) {
        if (signal.aborted && facts !== ALL_FACTS) requestedFacts = new Set([...requestedFacts ?? [], ...facts]);
        throw error;
      }
    },
    required: false,
  });
  const refreshFacts = async (facts: Iterable<RootFact>) => {
    requestedFacts = new Set([...requestedFacts ?? [], ...facts]);
    await query.refresh();
  };
  const displayed = () => query.displayed();
  const root = () => displayed()?.source.root ?? props.root;
  // Each fact keeps its identity across refreshes unless it changed.
  const git = createRetainedMemo(() => query.value()?.git ?? null);
  const inventory = createRetainedMemo(() => query.value()?.inventory ?? null);
  const agentContext = createRetainedMemo(() => query.value()?.agentContext ?? null);
  const live = useResidentLive();
  // Only a fact the host has not settled is polled.
  const pendingFacts = createMemo<RootFact[]>((previous = []) => {
    const facts: RootFact[] = [];
    if (query.value() !== undefined) {
      if (git()?.statusPending === true) facts.push("git");
      if (inventory()?.warming === true) facts.push("inventory");
    }
    return facts.length === previous.length && facts.every((fact, index) => previous[index] === fact) ? previous : facts;
  });
  const pendingRefresh = createAdaptivePoller(async () => {
    await refreshFacts(untrack(pendingFacts));
  }, 250, 2_000);
  onCleanup(pendingRefresh.cancel);
  createEffect(() => {
    pendingRefresh.setEnabled(live() && pendingFacts().length > 0);
  });
  // Hidden summaries revalidate on activation.
  const sourceRefresh = createBoundedDebouncedAsyncScheduler(async () => {
    if (untrack(live)) await refreshFacts(ALL_FACTS);
  }, SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS);
  onCleanup(sourceRefresh.cancel);
  onCleanup(subscribeSourceInvalidation((scope) => {
    if (scope.projectId === props.projectId) sourceRefresh.schedule();
  }));
  const loading = () => query.loading() && query.value() === undefined;
  const retaining = () => {
    const current = source();
    const visible = displayed()?.source;
    return (
      query.loading() &&
      current != null &&
      visible != null &&
      current.key !== visible.key
    );
  };
  const showRetainedWait = createPresentationWaiting(retaining);
  const label = () => root().label;
  const branchLabel = () => {
    if (loading() || git()?.statusPending) return "Checking…";
    if (!git()?.available) return "—";
    return (git()?.branch ?? "").trim() || "Detached HEAD";
  };
  const indexedFilesLabel = () => {
    if (loading()) return "Counting…";
    if (inventory()?.warming) return "Indexing…";
    const count = inventory()?.count;
    return count == null ? "—" : `${new Intl.NumberFormat().format(count)} indexed`;
  };
  // Rows keep their identity across index polls so the list does not remount.
  const resources = createMemo<Resource[]>((previous = []) => {
    const next = inventory()?.resources ?? [];
    if (
      next.length === previous.length &&
      next.every((resource, index) => {
        const held = previous[index];
        return held !== undefined && held.path === resource.path && held.label === resource.label && held.description === resource.description;
      })
    ) {
      return previous;
    }
    return next;
  });
  const contextCount = () =>
    (agentContext()?.instructions.length ?? 0) +
    (agentContext()?.skills.length ?? 0);
  const contextStatus = () => {
    const context = agentContext();
    if (!context) return "Unavailable";
    if (contextCount() > 0) return "Applied";
    if (!context.instructions_enabled && !context.skills_enabled) return "Off";
    return "None";
  };
  const emptyContextLabel = () => {
    const context = agentContext();
    if (!context) return "Agent context is unavailable.";
    if (context.instructions_enabled && context.skills_enabled) {
      return "No project instructions or skills apply at this root.";
    }
    return context.instructions_enabled
      ? "No project instructions apply at this root."
      : "No project skills apply at this root.";
  };
  const copyPath = () => {
    void copyTextToClipboard(root().path);
    setCopied(true);
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => setCopied(false), 1400);
  };

  const openResource = (resource: Resource) => {
    props.onOpenFile({
      rootId: root().id,
      rootLabel: label(),
      path: resource.path,
    });
  };

  return (
    <PreparedSurface
      class="den-files-root-preparation"
      name="files-root-summary"
      required={false}
      ready={() => !loading()}
      waiting="Preparing project overview…"
    >
    <Scrollport
      class="den-files-root-summary den-retained-presentation"
      contentAs="section"
      contentClass="den-files-root-summary__content"
      content={{ "aria-labelledby": "files-root-summary-title" }}
      data-testid="files-root-summary"
      data-retained={retaining() ? "true" : "false"}
      data-first-time-tip-anchor="files-project-roots"
      aria-hidden={retaining() ? "true" : undefined}
      inert={retaining() ? true : undefined}
    >
      <header class="den-files-root-summary__header flex items-start gap-2.5">
        <span class="den-files-root-summary__mark">
          <ThemeIcon slot="project-folder" size={24} />
        </span>
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-1.5">
            <h2
              id="files-root-summary-title"
              class="den-files-root-summary__name"
            >
              @{label()}
            </h2>
            <span
              class="den-status-mark"
              data-testid="files-root-summary-role"
            >
              {root().is_primary ? "Primary root" : "Additional root"}
            </span>
            <Show when={!loading() && !git()?.statusPending && git()?.available && git()?.dirty}>
              <button
                type="button"
                class="den-inline-control"
                data-testid="files-root-summary-working-tree"
                data-tone="warning"
                onClick={props.onOpenChanges}
              >
                {changedLabel(git())}
              </button>
            </Show>
            <Show when={!loading() && !git()?.statusPending && git()?.available && !git()?.dirty}>
              <span class="den-status-mark" data-tone="positive">
                Clean
              </span>
            </Show>
          </div>
          <p
            class="den-files-root-summary__path"
            data-testid="files-root-summary-path"
            data-tip={root().path}
            data-tip-when-clipped
          >
            {root().path}
          </p>
        </div>
        <div class="den-files-root-summary__actions box-border flex shrink-0 flex-wrap gap-1.5">
          <DenButton variant="ghost" compact onClick={copyPath}>
            {copied() ? "Copied" : "Copy path"}
          </DenButton>
          <OpenInButton compact target={{ absolutePath: root().path, projectRoots: props.roots.map((r) => r.path), entryKind: "folder" }} />
        </div>
      </header>

      <dl
        class="den-files-root-summary__facts"
        aria-label="Project root details"
      >
        <div class="grid min-w-0 grid-cols-[54px_minmax(0,1fr)] items-baseline gap-2">
          <dt>Branch</dt>
          <dd data-testid="files-root-summary-branch">
            {branchLabel()}
            <Show when={git()?.head_short}>
              {(head) => <span>{head()}</span>}
            </Show>
          </dd>
        </div>
        <div class="grid min-w-0 grid-cols-[54px_minmax(0,1fr)] items-baseline gap-2">
          <dt>Sync</dt>
          <dd data-testid="files-root-summary-sync">
            {syncLabel(git(), loading())}
          </dd>
        </div>
        <div class="grid min-w-0 grid-cols-[54px_minmax(0,1fr)] items-baseline gap-2">
          <dt>Files</dt>
          <dd data-testid="files-root-summary-file-count">
            {indexedFilesLabel()}
          </dd>
        </div>
        <div class="grid min-w-0 grid-cols-[54px_minmax(0,1fr)] items-baseline gap-2">
          <dt>Added</dt>
          <dd data-testid="files-root-summary-added">
            {formatSourceMtime(root().added_at)}
          </dd>
        </div>
      </dl>

      <Show when={!loading() && inventory()?.note}>
        <p class="den-search-status" role="status" data-testid="files-root-summary-coverage">
          {inventory()?.note}
          <Show when={!inventory()?.warming}>
            <DenButton variant="ghost" compact disabled={query.loading()} onClick={() => void refreshFacts(["inventory"])}>Retry file index</DenButton>
          </Show>
        </p>
      </Show>

      <Show when={!loading() && resources().length > 0}>
        <section
          class="den-files-root-summary__section"
          aria-labelledby="root-resources-title"
        >
          <div class="den-files-root-summary__section-heading flex min-h-[18px] items-center justify-between gap-2">
            <h3 id="root-resources-title">Project resources</h3>
          </div>
          <div class="den-files-root-summary__resources mt-1.5 grid grid-cols-2 gap-x-2 gap-y-1">
            <For each={resources()}>
              {(resource) => (
                <button
                  type="button"
                  class="den-files-root-summary__resource"
                  onClick={() => openResource(resource)}
                >
                  <span class="den-files-root-summary__resource-mark">MD</span>
                  <span class="den-files-root-summary__resource-copy flex min-w-0 flex-1 flex-col">
                    <strong>{resource.label}</strong>
                    <small>{resource.description}</small>
                  </span>
                  <span aria-hidden="true">→</span>
                </button>
              )}
            </For>
          </div>
        </section>
      </Show>

      <Show when={!loading()}>
        <section
          class="den-files-root-summary__section"
          aria-labelledby="root-context-title"
        >
          <div class="den-files-root-summary__section-heading flex min-h-[18px] items-center justify-between gap-2">
            <h3 id="root-context-title">Agent context</h3>
            <span
              class="den-status-mark"
              data-tone={contextCount() > 0 ? "positive" : undefined}
              data-testid="files-root-summary-context-status"
            >
              {contextStatus()}
            </span>
          </div>
          <Show when={agentContext()}>
            <Show when={!agentContext()?.instructions_enabled}>
              <p class="den-files-root-summary__muted">
                Project instructions are off.
              </p>
            </Show>
            <Show when={!agentContext()?.skills_enabled}>
              <p class="den-files-root-summary__muted">
                Project skills are off.
              </p>
            </Show>
            <Show when={contextCount() > 0}>
              <div class="mt-[7px] flex flex-wrap items-center gap-[5px]">
                <For each={agentContext()?.instructions ?? []}>
                  {(instruction) => (
                    <button
                      type="button"
                      class="den-inline-control"
                      data-testid="files-root-summary-agents-link"
                      onClick={() =>
                        openResource({
                          label: "AGENTS.md",
                          description: "Agent instructions",
                          path: instruction.path,
                        })
                      }
                    >
                      {instruction.path}
                    </button>
                  )}
                </For>
                <For each={agentContext()?.skills ?? []}>
                  {(skill) => (
                    <button
                      type="button"
                      class="den-inline-control"
                      data-tip={skill.description}
                      onClick={() =>
                        openResource({
                          label: skill.name,
                          description: skill.description,
                          path: skill.path,
                        })
                      }
                    >
                      {skill.name}
                    </button>
                  )}
                </For>
              </div>
            </Show>
            <Show
              when={
                contextCount() === 0 &&
                (agentContext()?.instructions_enabled || agentContext()?.skills_enabled)
              }
            >
              <p class="den-files-root-summary__muted">
                {emptyContextLabel()}
              </p>
            </Show>
          </Show>
          <Show when={agentContext() === null}>
            <p class="den-files-root-summary__muted">Agent context is unavailable.</p>
          </Show>
        </section>
      </Show>
    </Scrollport>
    <Show when={showRetainedWait()}>
      <div class="den-presentation-wait" role="status">
        Updating project overview…
      </div>
    </Show>
    </PreparedSurface>
  );
}
