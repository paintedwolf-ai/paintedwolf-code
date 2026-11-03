import { bindContextAction } from "../../context-actions.ts";
import { usePresentationParticipant } from "../../../ui/presentation-context.tsx";
import { Show, createEffect, createMemo, createSignal, onCleanup, onMount } from "solid-js";
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
import { BrowseOverflowMenu } from "../../browse/BrowseOverflowMenu.tsx";
import type { ContextMenuItem } from "../../ContextMenu.tsx";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { RecentAsksStrip } from "./RecentAsksStrip.tsx";
import {
  SavedApprovalElevatedSection,
  SavedApprovalGroupsSection,
  SavedApprovalSocketCreateSection,
  isSocketGrant,
} from "./SavedApprovalRows.tsx";

type Props = {
  client: LycaonClient;
  projectId?: string;
  sessionId?: string;
};

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

  const renderElevatedSection = () => (
    <SavedApprovalElevatedSection
      elevatedGrants={elevatedGrants()}
      elevatedQuiets={elevatedQuiets()}
      sessionId={props.sessionId}
      applicableCount={applicableIds().size}
      summaryError={summaryError()}
      actionsDisabled={actionsDisabled()}
      nowMs={nowMs()}
      onRevokeElevated={() => void revokeElevated()}
      onRevokeGrant={(grant) => void revokeOne(grant)}
      onRevokeQuiet={(quiet) => void revokeQuiet(quiet)}
    />
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
      <Show when={props.projectId && !loading() && !loadError()}>{renderElevatedSection()}</Show>

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
        <SavedApprovalSocketCreateSection
          socketPath={socketPath()}
          onSocketPathInput={(value) => {
            setSocketPath(value);
            setResolved(undefined);
          }}
          actionsDisabled={actionsDisabled()}
          resolveBusy={resolveBusy()}
          createBusy={createBusy()}
          onResolveSocket={() => void resolveSocket()}
          onCloseCreate={closeCreate}
          resolved={resolved()}
          socketScope={socketScope()}
          onSocketScopeChange={setSocketScope}
          projects={projects()}
          projectId={projectId()}
          onProjectIdChange={setProjectId}
          onCreateSocketGrant={() => void createSocketGrant()}
        />
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
              <Show when={!props.projectId}>{renderElevatedSection()}</Show>
              <SavedApprovalGroupsSection
                projectId={props.projectId}
                chatGroups={chatGroups()}
                projectGroups={projectGroups()}
                deviceGroup={deviceGroup()}
                quietGroups={quietGroups()}
                nowMs={nowMs()}
                actionsDisabled={actionsDisabled()}
                onRevokeGrant={(grant) => void revokeOne(grant)}
                onRevokeGroup={(group) => void revokeGroup(group)}
                onRevokeQuiet={(quiet) => void revokeQuiet(quiet)}
              />
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
