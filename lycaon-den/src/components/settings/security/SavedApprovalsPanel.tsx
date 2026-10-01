import { APPROVALS_COPY } from "../../../settings/security/approvals-copy.ts";
import { bindContextAction } from "../../context-actions.ts";
import { usePresentationParticipant } from "../../../ui/presentation-context.tsx";
import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  ApprovalGrant,
  ApprovalGrantScope,
  AskQuiet,
  Project,
  ResolveSocketGrantResponse,
} from "../../../api/types.ts";
import { confirmDestructive } from "../../../platform/interaction/confirm-dialog.ts";
import {
  approvalGrants,
  approvalGrantsError,
  approvalQuiets,
  isApprovalGrantsCacheLoaded,
  loadApprovalGrantsCache,
  refreshApprovalGrantsCache,
  retryApprovalGrantsLoad,
  revokeGrantsOptimistic,
} from "../../../settings/security/approval-grants-cache.ts";
import {
  expiredGrants,
  filterApprovalGrants,
  filterAskQuiets,
  formatGrantExpiry,
  formatGrantedAt,
  formatQuietExpiry,
  grantCategoryFilterOptions,
  groupApprovalGrants,
  groupAskQuiets,
  hasElevatedEffects,
  projectApprovalGrants,
  projectAskQuiets,
  unavailableGrants,
  type GrantCategoryFilter,
  type GrantScopeGroup,
} from "../../../settings/security/approval-grants-model.ts";
import {
  GRANT_KIND_LABELS,
  SAVED_APPROVALS_COPY,
} from "../../../settings/security/saved-approvals-copy.ts";
import { BrowseChrome } from "../../browse/BrowseChrome.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { BrowseOverflowMenu } from "../../browse/BrowseOverflowMenu.tsx";
import type { ContextMenuItem } from "../../ContextMenu.tsx";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { RecentAsksStrip } from "./RecentAsksStrip.tsx";

type Props = {
  client: LycaonClient;
  projectId?: string;
  sessionId?: string;
};

function isSocketGrant(grant: ApprovalGrant): boolean {
  return grant.category === "socket_path";
}

function unavailableBadgeLabel(grant: ApprovalGrant): string {
  return isSocketGrant(grant)
    ? SAVED_APPROVALS_COPY.unavailableSocketBadge
    : SAVED_APPROVALS_COPY.unavailableFolderBadge;
}

function projectName(project: Project): string {
  const name = project.name?.trim();
  if (name) return name;
  const root = project.roots[0]?.path?.trim();
  return root || project.id;
}

export function SavedApprovalsPanel(props: Props) {
  // A coarse clock keeps expiry labels current.
  const [nowMs, setNowMs] = createSignal(Date.now());
  onMount(() => {
    const tick = setInterval(() => setNowMs(Date.now()), 30_000);
    onCleanup(() => clearInterval(tick));
  });
  const [query, setQuery] = createSignal("");
  const [category, setCategory] = createSignal<GrantCategoryFilter>("all");
  const [actionError, setActionError] = createSignal<string | undefined>();
  const [revokeBusy, setRevokeBusy] = createSignal(false);

  const [createOpen, setCreateOpen] = createSignal(false);
  const [socketPath, setSocketPath] = createSignal("");
  const [socketScope, setSocketScope] = createSignal<ApprovalGrantScope>("project");
  const [projects, setProjects] = createSignal<Project[]>([]);
  const [projectId, setProjectId] = createSignal("");
  const [resolved, setResolved] = createSignal<ResolveSocketGrantResponse | undefined>();
  const [resolveBusy, setResolveBusy] = createSignal(false);
  const [createBusy, setCreateBusy] = createSignal(false);
  const [applicableIds, setApplicableIds] = createSignal<ReadonlySet<string>>(new Set());
  const [summaryError, setSummaryError] = createSignal(false);
  const [summaryRevision, setSummaryRevision] = createSignal(0);
  const [actionNotice, setActionNotice] = createSignal<string>();
  let summaryGeneration = 0;
  let summarySessionId: string | undefined;
  createEffect(() => {
    const sessionId = props.sessionId;
    summaryRevision();
    // Changes to the saved list may follow a revocation in another window.
    approvalGrants();
    approvalQuiets();
    const generation = ++summaryGeneration;
    if (sessionId !== summarySessionId) {
      summarySessionId = sessionId;
      setApplicableIds(new Set<string>());
      setSummaryError(false);
    }
    if (!sessionId) {
      setApplicableIds(new Set<string>());
      setSummaryError(false);
      return;
    }
    void props.client.getElevatedAccess(sessionId).then((summary) => {
      if (generation !== summaryGeneration) return;
      setApplicableIds(new Set(summary.records.map((row) => row.id)));
      setSummaryError(false);
    }).catch(() => {
      if (generation === summaryGeneration) setSummaryError(true);
    });
  });

  // Settings events refresh the cache; load failures wait for Retry.
  createEffect(() => {
    if (isApprovalGrantsCacheLoaded() || approvalGrantsError()) return;
    void loadApprovalGrantsCache(props.client);
  });

  const grants = () => props.projectId
    ? projectApprovalGrants(approvalGrants(), props.projectId, props.sessionId, applicableIds())
    : approvalGrants();
  const quiets = () => props.projectId
    ? projectAskQuiets(approvalQuiets(), props.sessionId, applicableIds())
    : approvalQuiets();
  const loadError = approvalGrantsError;
  const loading = () => !isApprovalGrantsCacheLoaded() && !loadError();

  const ordinaryGrants = createMemo(() => props.projectId
    ? grants().filter((grant) => grant.expired || !hasElevatedEffects(grant))
    : grants());
  const ordinaryQuiets = createMemo(() => props.projectId
    ? quiets().filter((quiet) => !hasElevatedEffects(quiet))
    : quiets());
  const hasOrdinary = () => ordinaryGrants().length > 0 || ordinaryQuiets().length > 0;

  const filtered = createMemo(() =>
    filterApprovalGrants(ordinaryGrants(), query(), category()),
  );
  const filteredQuiets = createMemo(() =>
    category() === "all" ? filterAskQuiets(ordinaryQuiets(), query()) : [],
  );
  const elevatedGrants = createMemo(() => (props.projectId ? grants() : filtered())
    .filter((grant) => !grant.expired && hasElevatedEffects(grant))
    .sort((a, b) => Date.parse(b.granted_at) - Date.parse(a.granted_at) || a.id.localeCompare(b.id)));
  const elevatedQuiets = createMemo(() => (props.projectId ? quiets() : filteredQuiets()).filter(hasElevatedEffects));
  const quietGroups = createMemo(() => groupAskQuiets(filteredQuiets().filter((row) => !hasElevatedEffects(row))));
  const groups = createMemo(() => groupApprovalGrants(filtered().filter((row) => row.expired || !hasElevatedEffects(row))));
  const chatGroups = createMemo(() =>
    groups().filter(
      (g): g is Extract<GrantScopeGroup, { kind: "chat" }> =>
        g.kind === "chat",
    ),
  );
  const projectGroups = createMemo(() =>
    groups().filter(
      (g): g is Extract<GrantScopeGroup, { kind: "project" }> =>
        g.kind === "project",
    ),
  );
  const deviceGroup = createMemo(() =>
    groups().find(
      (g): g is Extract<GrantScopeGroup, { kind: "device" }> =>
        g.kind === "device",
    ),
  );
  const chips = createMemo(() => grantCategoryFilterOptions(ordinaryGrants()));
  const missing = createMemo(() => unavailableGrants(grants()));
  const expired = createMemo(() => expiredGrants(grants()));

  const countLabel = createMemo(() => {
    const total = grants().length;
    const shown = filtered().length;
    if (query().trim() || category() !== "all") {
      return SAVED_APPROVALS_COPY.countFiltered(shown, total);
    }
    return SAVED_APPROVALS_COPY.countAll(total);
  });

  usePresentationParticipant("saved-approvals", () => !loading());
  const actionsDisabled = () => loading() || revokeBusy() || createBusy();

  const bulkRevoke = async (ids: string[]) => {
    if (ids.length === 0) return;
    setRevokeBusy(true);
    setActionError(undefined);
    try {
      const res = await props.client.revokeApprovalGrants({ ids });
      const revoked = res.results.filter((r) => r.revoked).map((r) => r.id);
      if (revoked.length > 0) revokeGrantsOptimistic(revoked);
      const failed = res.results.length - revoked.length;
      if (failed > 0) {
        setActionError(
          SAVED_APPROVALS_COPY.revokeSomeFailed(failed, res.results.length),
        );
      } else if (revoked.length > 0) {
        setActionNotice(SAVED_APPROVALS_COPY.revokedNotice);
      }
    } catch (err) {
      setActionError(
        err instanceof Error ? err.message : SAVED_APPROVALS_COPY.revokeError,
      );
    } finally {
      setRevokeBusy(false);
    }
  };

  const revokeElevated = async () => {
    if (!props.sessionId || summaryError()) return;
    if (!(await confirmDestructive({
      title: SAVED_APPROVALS_COPY.revokeElevatedTitle,
      message: SAVED_APPROVALS_COPY.revokeElevatedConfirm,
      okLabel: SAVED_APPROVALS_COPY.revokeButton,
    }))) return;
    setRevokeBusy(true);
    setActionError(undefined);
    setActionNotice(undefined);
    try {
      const response = await props.client.revokeElevatedAccess(props.sessionId);
      const removed = response.results.filter((result) => result.disposition !== "failed").map((result) => result.id);
      revokeGrantsOptimistic(removed);
      if (response.results.some((result) => result.disposition === "failed")) {
        setActionError(SAVED_APPROVALS_COPY.revokeElevatedPartial);
      } else {
        setActionNotice(SAVED_APPROVALS_COPY.revokeElevatedSuccess);
      }
    } catch {
      setActionError(SAVED_APPROVALS_COPY.revokeError);
    } finally {
      setRevokeBusy(false);
    }
  };

  const revokeOne = async (grant: ApprovalGrant) => {
    if (isSocketGrant(grant) && !grant.expired) {
      const detail =
        grant.revoke_applies_to?.trim() ||
        SAVED_APPROVALS_COPY.socketRevokeConfirm;
      if (
        !(await confirmDestructive({
          message: detail,
          title: SAVED_APPROVALS_COPY.socketRevokeTitle,
          okLabel: SAVED_APPROVALS_COPY.revokeButton,
        }))
      ) {
        return;
      }
    }
    await bulkRevoke([grant.id]);
  };

  const revokeQuiet = async (quiet: AskQuiet) => {
    await bulkRevoke([quiet.id]);
  };

  const revokeGroup = async (group: GrantScopeGroup) => {
    if (
      !(await confirmDestructive({
        message: SAVED_APPROVALS_COPY.revokeGroupConfirm,
        title: SAVED_APPROVALS_COPY.revokeButton,
        okLabel: SAVED_APPROVALS_COPY.revokeButton,
      }))
    ) {
      return;
    }
    await bulkRevoke(group.grants.map((g) => g.id));
  };

  const revokeMissing = async () => {
    const ids = missing().map((g) => g.id);
    if (ids.length === 0) return;
    if (
      !(await confirmDestructive({
        message: SAVED_APPROVALS_COPY.revokeMissingConfirm,
        title: SAVED_APPROVALS_COPY.revokeButton,
        okLabel: SAVED_APPROVALS_COPY.revokeButton,
      }))
    ) {
      return;
    }
    await bulkRevoke(ids);
  };

  const clearExpired = async () => {
    const ids = expired().map((g) => g.id);
    if (ids.length === 0) return;
    if (
      !(await confirmDestructive({
        message: SAVED_APPROVALS_COPY.clearExpiredConfirm,
        title: SAVED_APPROVALS_COPY.clearButton,
        okLabel: SAVED_APPROVALS_COPY.clearButton,
      }))
    ) {
      return;
    }
    await bulkRevoke(ids);
  };

  const openCreate = () => {
    setCreateOpen(true);
    setActionError(undefined);
    void props.client
      .listProjects()
      .then((list) => {
        setProjects(list);
        if (!projectId() && list[0]) setProjectId(list[0].id);
      })
      .catch(() => setProjects([]));
  };

  const closeCreate = () => {
    setCreateOpen(false);
    setSocketPath("");
    setResolved(undefined);
  };

  const resolveSocket = async () => {
    setResolveBusy(true);
    setActionError(undefined);
    setResolved(undefined);
    try {
      const res = await props.client.resolveSocketGrant({
        socket_path: socketPath().trim(),
      });
      setResolved(res);
    } catch (err) {
      setActionError(
        err instanceof Error ? err.message : SAVED_APPROVALS_COPY.socketResolveError,
      );
    } finally {
      setResolveBusy(false);
    }
  };

  const createSocketGrant = async () => {
    const preview = resolved();
    if (!preview) return;
    if (socketScope() === "project" && !projectId()) {
      setActionError(SAVED_APPROVALS_COPY.socketProjectMissing);
      return;
    }
    setCreateBusy(true);
    setActionError(undefined);
    try {
      await props.client.createApprovalGrant({
        category: "socket_path",
        scope: socketScope(),
        socket_path: preview.approved_path,
        project_id: socketScope() === "project" ? projectId() : undefined,
      });
      closeCreate();
      refreshApprovalGrantsCache();
    } catch (err) {
      setActionError(
        err instanceof Error ? err.message : SAVED_APPROVALS_COPY.socketCreateError,
      );
    } finally {
      setCreateBusy(false);
    }
  };

  const overflowItems = (): ContextMenuItem[] => {
    const items: ContextMenuItem[] = [];
    if (expired().length > 0) {
      items.push(bindContextAction({
        label: SAVED_APPROVALS_COPY.clearExpired(expired().length),
        testId: "saved-approvals-clear-expired",
        disabled: actionsDisabled(),
        onSelect: () => void clearExpired(),
      }));
    }
    if (missing().length > 0) {
      items.push(bindContextAction({
        label: SAVED_APPROVALS_COPY.revokeMissing(missing().length),
        testId: "saved-approvals-revoke-missing",
        danger: true,
        disabled: actionsDisabled(),
        onSelect: () => void revokeMissing(),
      }));
    }
    return items;
  };

  const grantRow = (grant: ApprovalGrant, showScope = false) => (
    <li
      class="den-saved-approvals-row"
      classList={{ "den-saved-approvals-row--expired": grant.expired === true }}
      data-testid="saved-approvals-row"
      data-grant-id={grant.id}
      data-grant-category={grant.category}
    >
      <span class="den-saved-approvals-kind">
        {GRANT_KIND_LABELS[grant.category]}
      </span>
      <span class="den-saved-approvals-main">
        <Show
          when={grant.category === "action_set"}
          fallback={
            <Show
              when={grant.category === "host_resource"}
              fallback={
                <span class="den-saved-approvals-what">
                  <code data-tip={grant.secret_names?.join(", ") || grant.pattern} data-tip-when-clipped>
                    {grant.secret_names?.join(", ") || grant.pattern}
                  </code>
                  {rowBadges(grant)}
                </span>
              }
            >
              <span class="den-saved-approvals-what">
                <For each={grant.resource_ids ?? []}>
                  {(id) => <span class="den-saved-approvals-rchip">{id}</span>}
                </For>
                {rowBadges(grant)}
              </span>
            </Show>
          }
        >
          <span
            class="den-saved-approvals-what"
            data-testid="saved-approvals-actionset-summary"
          >
            {SAVED_APPROVALS_COPY.actionSetSummary(grant.action_count ?? 1)}
            {rowBadges(grant)}
          </span>
        </Show>
        <Show when={showScope}><small class="den-saved-approvals-detail">{SAVED_APPROVALS_COPY.scopeLabel(grant.scope, !!grant.project_id && grant.scope === "device")}</small></Show>
        <small class="den-saved-approvals-detail">{rowDetail(grant)}</small>
        <Show when={(grant.secret_recipients?.length ?? 0) > 0}>
          <small class="den-saved-approvals-detail" data-testid="saved-secret-recipients">
            {grant.secret_recipients?.map((recipient) => recipient.label).join("; ")}
          </small>
        </Show>
      </span>
      <span class="den-saved-approvals-dates" data-testid="saved-approvals-granted-cell">
        {formatGrantedAt(grant.granted_at)}
        <Show when={grant.expires_at}>
          {(expires) => (
            <small data-testid="saved-approvals-expiry">
              {grant.expired
                ? SAVED_APPROVALS_COPY.expiredBadge.toLowerCase()
                : "expires"}{" "}
              {formatGrantExpiry(expires(), nowMs())}
            </small>
          )}
        </Show>
      </span>
      <DenButton
        variant="danger"
        compact
        data-testid="saved-approvals-revoke"
        disabled={actionsDisabled()}
        onClick={() => void revokeOne(grant)}
      >
        {grant.expired
          ? SAVED_APPROVALS_COPY.clearButton
          : SAVED_APPROVALS_COPY.revokeButton}
      </DenButton>
    </li>
  );

  const reviewGrantRow = (grant: ApprovalGrant) => (
    <li class="den-settings-pref-row den-saved-approvals-priority-row" classList={{ "den-saved-approvals-row--expired": grant.expired === true }} data-testid="saved-approvals-row" data-grant-id={grant.id} data-grant-category={grant.category}>
      <div class="den-saved-approvals-priority-content">
        <span class="den-saved-approvals-priority-title">{grant.title} {rowBadges(grant)}</span>
        <span class="den-saved-approvals-priority-meta">
          {SAVED_APPROVALS_COPY.scopeLabel(grant.scope, !!grant.project_id && grant.scope === "device")}
          <span aria-hidden="true">·</span>
          {rowDetail(grant) || GRANT_KIND_LABELS[grant.category]}
        </span>
        <Show when={grant.pattern && grant.category !== "action_set" && grant.category !== "host_resource"}>
          <code class="den-saved-approvals-priority-pattern">{grant.secret_names?.join(", ") || grant.pattern}</code>
        </Show>
        <Show when={(grant.secret_recipients?.length ?? 0) > 0}>
          <span class="den-saved-approvals-priority-detail" data-testid="saved-secret-recipients">{grant.secret_recipients?.map((recipient) => recipient.label).join("; ")}</span>
        </Show>
        <Show when={grant.expires_at}>
          {(expires) => <span class="den-saved-approvals-priority-detail" data-testid="saved-approvals-expiry">{grant.expired ? SAVED_APPROVALS_COPY.expiredBadge.toLowerCase() : "expires"} {formatGrantExpiry(expires(), nowMs())}</span>}
        </Show>
      </div>
      <DenButton variant="secondary" data-testid="saved-approvals-revoke" disabled={actionsDisabled()} onClick={() => void revokeOne(grant)}>
        {grant.expired ? SAVED_APPROVALS_COPY.clearButton : SAVED_APPROVALS_COPY.revokeButton}
      </DenButton>
    </li>
  );

  const rowBadges = (grant: ApprovalGrant) => (
    <>
      <Show when={grant.unavailable}>
        <span
          class="den-saved-approvals-badge den-saved-approvals-badge--warn"
          data-testid="saved-approvals-unavailable"
        >
          {unavailableBadgeLabel(grant)}
        </span>
      </Show>
      <Show when={grant.repointed}>
        <span
          class="den-saved-approvals-badge den-saved-approvals-badge--warn"
          data-testid="saved-approvals-repointed"
        >
          {SAVED_APPROVALS_COPY.repointedBadge}
        </span>
      </Show>
      <Show when={grant.expired}>
        <span
          class="den-saved-approvals-badge"
          data-testid="saved-approvals-expired"
        >
          {SAVED_APPROVALS_COPY.expiredBadge}
        </span>
      </Show>
    </>
  );

  const rowDetail = (grant: ApprovalGrant): string => {
    const withScope = (detail: string): string => {
      if (grant.scope !== "device") return detail;
      const scope = grant.project_dir
        ? SAVED_APPROVALS_COPY.deviceProjectOnly
        : SAVED_APPROVALS_COPY.deviceAllProjects;
      return detail ? `${detail} · ${scope}` : scope;
    };
    if (isSocketGrant(grant)) {
      if (grant.unavailable) return withScope(SAVED_APPROVALS_COPY.socketMissingHint);
      const resolvedPath = grant.resolved_path?.trim();
      const approvedPath = grant.approved_path?.trim();
      if (resolvedPath && resolvedPath !== approvedPath) {
        return withScope(`${SAVED_APPROVALS_COPY.socketDetailPrefix} · ${SAVED_APPROVALS_COPY.socketResolvesTo} ${resolvedPath}`);
      }
      return withScope(SAVED_APPROVALS_COPY.socketDetailPrefix);
    }
    if (grant.category === "host_resource") {
      return withScope(SAVED_APPROVALS_COPY.resourceIdsHint);
    }
    if (grant.category === "direct_ip") {
      return withScope(SAVED_APPROVALS_COPY.directIPDetail);
    }
    return withScope(grant.coverage);
  };

  const groupRows = (group: GrantScopeGroup) => (
    <ul class="den-saved-approvals-list" data-testid="saved-approvals-table">
      <For each={group.grants}>{(grant) => props.projectId ? reviewGrantRow(grant) : grantRow(grant)}</For>
    </ul>
  );

  const elevatedSection = () => (
    <Show when={elevatedGrants().length > 0 || elevatedQuiets().length > 0}>
      <section class="den-saved-approvals-band" data-testid="saved-approvals-band-elevated">
        <div class="den-saved-approvals-band-head den-saved-approvals-band-head--elevated">
          <h3>{APPROVALS_COPY.elevated.label} <span class="den-saved-approvals-priority-count">{elevatedGrants().length + elevatedQuiets().length}</span></h3>
          <Show when={props.sessionId && applicableIds().size > 0 && !summaryError()}>
            <DenButton variant="secondary" compact disabled={actionsDisabled()} data-testid="saved-approvals-revoke-elevated" onClick={() => void revokeElevated()}>
              {SAVED_APPROVALS_COPY.revokeElevatedAll}
            </DenButton>
          </Show>
        </div>
        <p class="den-saved-approvals-priority-hint">{SAVED_APPROVALS_COPY.elevatedHint}</p>
        <ul class="den-settings-pref-group den-saved-approvals-priority-list" data-testid="saved-approvals-elevated-list">
          <For each={elevatedGrants()}>{(grant) => reviewGrantRow(grant)}</For>
          <For each={elevatedQuiets()}>{(quiet) =>
            <li class="den-settings-pref-row den-saved-approvals-priority-row" data-testid="saved-approvals-quiet-row" data-quiet-id={quiet.id}>
              <span class="den-saved-approvals-priority-content">
                <span class="den-saved-approvals-priority-title">{quiet.label}</span>
                <span class="den-saved-approvals-priority-meta">
                  {SAVED_APPROVALS_COPY.scopeLabel("chat", false)} <span aria-hidden="true">·</span> {SAVED_APPROVALS_COPY.quietedGroupHeading}
                </span>
                <span class="den-saved-approvals-priority-detail">{quiet.expires_at
                  ? SAVED_APPROVALS_COPY.quietedUntil(formatQuietExpiry(quiet, nowMs()))
                  : SAVED_APPROVALS_COPY.quietedChat} · {SAVED_APPROVALS_COPY.quietedSuppressed(quiet.suppressed)}</span>
              </span>
              <DenButton variant="secondary" data-testid="saved-approvals-revoke-quiet" disabled={actionsDisabled()} onClick={() => void revokeQuiet(quiet)}>
                {SAVED_APPROVALS_COPY.revokeButton}
              </DenButton>
            </li>
          }</For>
        </ul>
      </section>
    </Show>
  );

  const searchControl = () => <DenInput
    data-testid="saved-approvals-search"
    type="search"
    aria-label={SAVED_APPROVALS_COPY.searchAriaLabel}
    placeholder={SAVED_APPROVALS_COPY.searchPlaceholder}
    value={query()}
    onInput={(event) => setQuery(event.currentTarget.value)}
  />;
  const categoryControl = () => chips().length > 1 ? <BrowseSegmented
    ariaLabel={SAVED_APPROVALS_COPY.categoryFilterLabel}
    testId="saved-approvals-category"
    value={category()}
    onChange={(id) => setCategory(id as GrantCategoryFilter)}
    options={chips().map((chip) => ({
      id: chip.id,
      label: chip.id === "all"
        ? `${SAVED_APPROVALS_COPY.categoryAll} · ${chip.count}`
        : `${GRANT_KIND_LABELS[chip.id]} · ${chip.count}`,
      testId: `saved-approvals-category-${chip.id}`,
    }))}
  /> : undefined;
  const overflowControl = () => <Show when={overflowItems().length > 0}>
    <BrowseOverflowMenu testId="saved-approvals-overflow" items={overflowItems()} />
  </Show>;

  return (
    <div class="den-saved-approvals" classList={{ "den-saved-approvals--project": !!props.projectId }} data-testid="saved-approvals-panel">
      <Show when={props.projectId} fallback={
        <aside class="den-approvals-explain" role="note" data-testid="saved-approvals-explain">
          <strong class="den-approvals-explain-title">{SAVED_APPROVALS_COPY.explainTitle}</strong>
          <p class="den-settings-hint">{SAVED_APPROVALS_COPY.explain}</p>
        </aside>
      }>
        <p class="den-settings-hint" data-testid="saved-approvals-project-explain">
          {props.sessionId ? SAVED_APPROVALS_COPY.projectExplain : SAVED_APPROVALS_COPY.projectExplainNoChat}
        </p>
      </Show>

      <Show when={!props.projectId}><RecentAsksStrip client={props.client} /></Show>
      <Show when={props.projectId && !loading() && !loadError()}>{elevatedSection()}</Show>

      <Show when={props.projectId} fallback={
        <BrowseChrome
          variant="embed"
          testId="saved-approvals-toolbar"
          primary={searchControl()}
          trailing={<DenButton
            variant="secondary"
            data-testid="saved-approvals-add-socket"
            disabled={actionsDisabled() || createOpen()}
            onClick={openCreate}
          >{SAVED_APPROVALS_COPY.addLocalService}</DenButton>}
          chips={categoryControl()}
          overflow={overflowControl()}
          meta={<Show when={!loading() && !loadError()}><span data-testid="saved-approvals-count">{countLabel()}</span></Show>}
        />
      }>
        <Show when={hasOrdinary()}>
          <section class="den-saved-approvals-other" aria-label={SAVED_APPROVALS_COPY.otherHeading}>
            <h3 class="den-saved-approvals-other-heading">{SAVED_APPROVALS_COPY.otherHeading}</h3>
            <BrowseChrome
              variant="embed"
              testId="saved-approvals-toolbar"
              primary={searchControl()}
              chips={categoryControl()}
              overflow={overflowControl()}
            />
          </section>
        </Show>
      </Show>

      <Show when={createOpen()}>
        <section
          class="den-saved-approvals-create"
          data-testid="saved-approvals-socket-create"
          aria-labelledby="saved-approvals-socket-create-title"
        >
          <h3
            id="saved-approvals-socket-create-title"
            class="den-settings-subhead"
            {...chromeProps()}
          >
            {SAVED_APPROVALS_COPY.socketCreateTitle}
          </h3>
          <p class="den-settings-hint">
            {SAVED_APPROVALS_COPY.socketSectionExplain}
          </p>
          <label class="den-settings-field">
            <span>{SAVED_APPROVALS_COPY.socketPathLabel}</span>
            <DenInput
              data-testid="saved-approvals-socket-path"
              type="text"
              aria-label={SAVED_APPROVALS_COPY.socketPathAriaLabel}
              placeholder={SAVED_APPROVALS_COPY.socketPathPlaceholder}
              value={socketPath()}
              onInput={(e) => {
                setSocketPath(e.currentTarget.value);
                setResolved(undefined);
              }}
            />
          </label>
          <div class="den-saved-approvals-create-actions">
            <DenButton
              variant="secondary"
              data-testid="saved-approvals-socket-resolve"
              disabled={actionsDisabled() || resolveBusy() || !socketPath().trim()}
              onClick={() => void resolveSocket()}
            >
              {SAVED_APPROVALS_COPY.socketResolve}
            </DenButton>
            <DenButton
              variant="secondary"
              data-testid="saved-approvals-socket-cancel"
              disabled={createBusy()}
              onClick={closeCreate}
            >
              {SAVED_APPROVALS_COPY.socketCancel}
            </DenButton>
          </div>
          <Show when={resolved()} keyed>
            {(preview) => (
              <div
                class="den-saved-approvals-create-confirm"
                data-testid="saved-approvals-socket-confirm"
                role="group"
                aria-label="Confirm local service grant"
              >
                <p data-testid="saved-approvals-socket-approved">
                  {SAVED_APPROVALS_COPY.socketApprovedLabel}:{" "}
                  <code>{preview.approved_path}</code>
                </p>
                <p data-testid="saved-approvals-socket-resolved">
                  {SAVED_APPROVALS_COPY.socketResolvedLabel}:{" "}
                  <code>{preview.resolved_path}</code>
                </p>
                <p
                  class="den-settings-warn"
                  data-testid="saved-approvals-socket-authority"
                  role="note"
                >
                  {preview.authority_warning}
                </p>
                <BrowseSegmented
                  ariaLabel={SAVED_APPROVALS_COPY.socketScopeLabel}
                  testId="saved-approvals-socket-scope"
                  value={socketScope()}
                  onChange={(id) => setSocketScope(id as ApprovalGrantScope)}
                  options={[
                    {
                      id: "project",
                      label: SAVED_APPROVALS_COPY.socketScopeProject,
                      testId: "saved-approvals-socket-scope-project",
                    },
                    {
                      id: "device",
                      label: SAVED_APPROVALS_COPY.socketScopeDevice,
                      testId: "saved-approvals-socket-scope-device",
                    },
                  ]}
                />
                <Show when={socketScope() === "project"}>
                  <label class="den-settings-field">
                    <span>{SAVED_APPROVALS_COPY.socketProjectLabel}</span>
                    <Show
                      when={projects().length > 0}
                      fallback={
                        <p class="den-settings-hint">
                          {SAVED_APPROVALS_COPY.socketProjectMissing}
                        </p>
                      }
                    >
                      <DenSelect
                        aria-label="Project"
                        data-testid="saved-approvals-socket-project"
                        value={projectId()}
                        options={projects().map((project) => ({
                          value: project.id,
                          label: projectName(project),
                        }))}
                        onValueChange={setProjectId}
                      />
                    </Show>
                  </label>
                </Show>
                <div class="den-saved-approvals-create-actions">
                  <DenButton
                    variant="primary"
                    data-testid="saved-approvals-socket-confirm-create"
                    disabled={actionsDisabled()}
                    onClick={() => void createSocketGrant()}
                  >
                    {SAVED_APPROVALS_COPY.socketConfirm}
                  </DenButton>
                </div>
              </div>
            )}
          </Show>
        </section>
      </Show>

      <Show when={loadError()}>
        {(msg) => (
          <p
            class="den-settings-warn"
            role="alert"
            data-testid="saved-approvals-error"
          >
            {msg()}{" "}
            <DenButton
              variant="secondary"
              compact
              data-testid="saved-approvals-retry"
              onClick={retryApprovalGrantsLoad}
            >
              {SAVED_APPROVALS_COPY.retry}
            </DenButton>
          </p>
        )}
      </Show>

      <Show when={actionError()}>
        {(msg) => (
          <p
            class="den-settings-warn"
            role="alert"
            data-testid="saved-approvals-action-error"
          >
            {msg()}
          </p>
        )}
      </Show>
      <Show when={actionNotice()}>
        {(message) => <p class="den-settings-hint" role="status" data-testid="saved-approvals-action-notice">{message()}</p>}
      </Show>
      <Show when={summaryError()}>
        <p class="den-settings-warn" role="alert" data-testid="saved-approvals-summary-error">
          {SAVED_APPROVALS_COPY.summaryError}
          <DenButton variant="link" onClick={() => setSummaryRevision((value) => value + 1)}>{SAVED_APPROVALS_COPY.retry}</DenButton>
        </p>
      </Show>

      <Show when={loading()}>
        <p
          class="den-settings-hint"
          role="status"
          data-testid="saved-approvals-loading"
        >
          {SAVED_APPROVALS_COPY.loading}
        </p>
      </Show>

      <Show when={!loading() && !loadError()}>
        <Show
          when={grants().length > 0 || quiets().length > 0}
          fallback={
            <p
              class="den-saved-approvals-empty"
              data-testid="saved-approvals-empty"
            >
              {SAVED_APPROVALS_COPY.empty}
            </p>
          }
        >
          <Show when={!props.projectId || hasOrdinary()}>
          <Show
            when={filtered().length > 0 || filteredQuiets().length > 0}
            fallback={
              <p
                class="den-saved-approvals-empty"
                data-testid="saved-approvals-no-matches"
              >
                {SAVED_APPROVALS_COPY.noMatches}
              </p>
            }
          >
            <Show when={!props.projectId}>{elevatedSection()}</Show>
            <Show when={chatGroups().length > 0}>
              <section
                class="den-saved-approvals-band"
                data-testid="saved-approvals-band-chat"
              >
                <div class="den-saved-approvals-band-head">
                  <h3>
                    <span
                      class="den-saved-approvals-livedot"
                      aria-hidden="true"
                    />
                    {SAVED_APPROVALS_COPY.chatGroupHeading}
                  </h3>
                  <span class="den-saved-approvals-lifetime">
                    {SAVED_APPROVALS_COPY.chatGroupLifetime}
                  </span>
                </div>
                <For each={chatGroups()}>
                  {(group) => (
                    <div
                      class="den-saved-approvals-session"
                      data-testid="saved-approvals-session"
                      data-session-id={group.sessionId}
                    >
                      <div class="den-saved-approvals-session-head">
                        <span class="den-saved-approvals-session-name">
                          {group.sessionTitle ||
                            SAVED_APPROVALS_COPY.chatGroupUntitled}
                        </span>
                        <span class="den-saved-approvals-session-kind">
                          · {SAVED_APPROVALS_COPY.chatGroupKind}
                        </span>
                        <button
                          type="button"
                          class="den-saved-approvals-group-revoke"
                          data-testid="saved-approvals-revoke-group"
                          disabled={actionsDisabled()}
                          onClick={() => void revokeGroup(group)}
                        >
                          {SAVED_APPROVALS_COPY.revokeGroupChat}
                        </button>
                      </div>
                      {groupRows(group)}
                    </div>
                  )}
                </For>
              </section>
            </Show>

            <For each={projectGroups()}>
              {(group) => (
                <section
                  class="den-saved-approvals-band"
                  data-testid="saved-approvals-band-project"
                >
                  <div class="den-saved-approvals-band-head">
                    <h3>{SAVED_APPROVALS_COPY.projectGroupHeading}</h3>
                    <span class="den-saved-approvals-lifetime">
                      {SAVED_APPROVALS_COPY.projectGroupLifetime}
                      <Show when={group.projectDir}>
                        {" · "}
                        <code>{group.projectDir}</code>
                      </Show>
                    </span>
                    <button
                      type="button"
                      class="den-saved-approvals-group-revoke"
                      data-testid="saved-approvals-revoke-group"
                      disabled={actionsDisabled()}
                      onClick={() => void revokeGroup(group)}
                    >
                      {SAVED_APPROVALS_COPY.revokeGroupProject}
                    </button>
                  </div>
                  <div class="den-saved-approvals-card">{groupRows(group)}</div>
                </section>
              )}
            </For>

            <Show when={deviceGroup()} keyed>
              {(group) => (
                <section
                  class="den-saved-approvals-band"
                  data-testid="saved-approvals-band-device"
                >
                  <div class="den-saved-approvals-band-head">
                    <h3>{SAVED_APPROVALS_COPY.deviceGroupHeading}</h3>
                    <span class="den-saved-approvals-lifetime">
                      {SAVED_APPROVALS_COPY.deviceGroupLifetime}
                    </span>
                    <button
                      type="button"
                      class="den-saved-approvals-group-revoke"
                      data-testid="saved-approvals-revoke-group"
                      disabled={actionsDisabled()}
                      onClick={() => void revokeGroup(group)}
                    >
                      {SAVED_APPROVALS_COPY.revokeGroupDevice}
                    </button>
                  </div>
                  <div class="den-saved-approvals-card">{groupRows(group)}</div>
                </section>
              )}
            </Show>

            <Show when={quietGroups().length > 0}>
              <section
                class="den-saved-approvals-band"
                data-testid="saved-approvals-band-quieted"
              >
                <div class="den-saved-approvals-band-head">
                  <h3>{SAVED_APPROVALS_COPY.quietedGroupHeading}</h3>
                  <span class="den-saved-approvals-lifetime">
                    {SAVED_APPROVALS_COPY.quietedGroupLifetime}
                  </span>
                </div>
                <For each={quietGroups()}>
                  {(group) => (
                    <div
                      class="den-saved-approvals-session"
                      data-testid="saved-approvals-quiet-session"
                      data-session-id={group.sessionId}
                    >
                      <div class="den-saved-approvals-session-head">
                        <span class="den-saved-approvals-session-name">
                          {group.sessionTitle ||
                            SAVED_APPROVALS_COPY.quietedUntitled}
                        </span>
                      </div>
                      <ul
                        class="den-saved-approvals-list"
                        data-testid="saved-approvals-quiet-list"
                      >
                        <For each={group.quiets}>
                          {(quiet) => (
                            <li
                              class="den-settings-pref-row den-saved-approvals-priority-row"
                              data-testid="saved-approvals-quiet-row"
                              data-quiet-id={quiet.id}
                            >
                              <div class="den-saved-approvals-priority-content">
                                <span class="den-saved-approvals-priority-title">
                                  {quiet.label}
                                </span>
                                <p class="den-settings-hint">
                                  {quiet.expires_at
                                    ? SAVED_APPROVALS_COPY.quietedUntil(
                                        formatQuietExpiry(quiet, nowMs()),
                                      )
                                    : SAVED_APPROVALS_COPY.quietedChat}
                                  {" · "}
                                  {SAVED_APPROVALS_COPY.quietedSuppressed(
                                    quiet.suppressed,
                                  )}
                                </p>
                              </div>
                              <DenButton
                                variant="secondary"
                                data-testid="saved-approvals-revoke-quiet"
                                disabled={actionsDisabled()}
                                onClick={() => void revokeQuiet(quiet)}
                              >
                                {SAVED_APPROVALS_COPY.revokeButton}
                              </DenButton>
                            </li>
                          )}
                        </For>
                      </ul>
                    </div>
                  )}
                </For>
              </section>
            </Show>
          </Show>
          </Show>
        </Show>
      </Show>

      <Show when={!props.projectId}>
        <p class="den-settings-hint den-saved-approvals-tip">
          {SAVED_APPROVALS_COPY.chickletTip}
        </p>
      </Show>
    </div>
  );
}
