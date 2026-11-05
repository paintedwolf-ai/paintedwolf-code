import { For, Show } from "solid-js";
import type {
  ApprovalGrant,
  ApprovalGrantScope,
  AskQuiet,
  Project,
  ResolveSocketGrantResponse,
} from "../../../api/types.ts";
import { APPROVALS_COPY } from "../../../settings/security/approvals-copy.ts";
import {
  formatGrantExpiry,
  formatGrantedAt,
  formatQuietExpiry,
  type GrantScopeGroup,
  type groupAskQuiets,
} from "../../../settings/security/approval-grants-model.ts";
import {
  GRANT_KIND_LABELS,
  SAVED_APPROVALS_COPY,
} from "../../../settings/security/saved-approvals-copy.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";

type QuietScopeGroup = ReturnType<typeof groupAskQuiets>[number];

export function isSocketGrant(grant: ApprovalGrant): boolean {
  return grant.category === "socket_path";
}

function unavailableBadgeLabel(grant: ApprovalGrant): string {
  return isSocketGrant(grant)
    ? SAVED_APPROVALS_COPY.unavailableSocketBadge
    : SAVED_APPROVALS_COPY.unavailableFolderBadge;
}

export function projectName(project: Project): string {
  const name = project.name?.trim();
  if (name) return name;
  const root = project.roots[0]?.path?.trim();
  return root || project.id;
}

function renderRowBadges(grant: ApprovalGrant) {
  return (
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
}

function formatRowDetail(grant: ApprovalGrant): string {
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
      return withScope(
        `${SAVED_APPROVALS_COPY.socketDetailPrefix} · ${SAVED_APPROVALS_COPY.socketResolvesTo} ${resolvedPath}`,
      );
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
}

export function SavedApprovalGrantRow(props: {
  grant: ApprovalGrant;
  showScope?: boolean;
  nowMs: number;
  actionsDisabled: boolean;
  onRevoke: (grant: ApprovalGrant) => void;
}) {
  return (
    <li
      data-testid="saved-approvals-row"
      data-grant-id={props.grant.id}
      data-grant-category={props.grant.category}
    >
      <div
        class="den-saved-approvals-row"
        classList={{ "den-saved-approvals-row--expired": props.grant.expired === true }}
      >
        <span class="den-saved-approvals-kind">
          {GRANT_KIND_LABELS[props.grant.category]}
        </span>
        <span class="den-saved-approvals-main">
          <Show
            when={props.grant.category === "action_set"}
            fallback={
              <Show
                when={props.grant.category === "host_resource"}
                fallback={
                  <span class="den-saved-approvals-what">
                    <code
                      data-tip={props.grant.secret_names?.join(", ") || props.grant.pattern}
                      data-tip-when-clipped
                    >
                      {props.grant.secret_names?.join(", ") || props.grant.pattern}
                    </code>
                    {renderRowBadges(props.grant)}
                  </span>
                }
              >
                <span class="den-saved-approvals-what">
                  <For each={props.grant.resource_ids ?? []}>
                    {(id) => <span class="den-saved-approvals-rchip">{id}</span>}
                  </For>
                  {renderRowBadges(props.grant)}
                </span>
              </Show>
            }
          >
            <span
              class="den-saved-approvals-what"
              data-testid="saved-approvals-actionset-summary"
            >
              {SAVED_APPROVALS_COPY.actionSetSummary(props.grant.action_count ?? 1)}
              {renderRowBadges(props.grant)}
            </span>
          </Show>
          <Show when={props.showScope}>
            <small class="den-saved-approvals-detail">
              {SAVED_APPROVALS_COPY.scopeLabel(
                props.grant.scope,
                !!props.grant.project_id && props.grant.scope === "device",
              )}
            </small>
          </Show>
          <small class="den-saved-approvals-detail">{formatRowDetail(props.grant)}</small>
          <Show when={(props.grant.secret_recipients?.length ?? 0) > 0}>
            <small class="den-saved-approvals-detail" data-testid="saved-secret-recipients">
              {props.grant.secret_recipients?.map((recipient) => recipient.label).join("; ")}
            </small>
          </Show>
        </span>
        <span class="den-saved-approvals-dates" data-testid="saved-approvals-granted-cell">
          {formatGrantedAt(props.grant.granted_at)}
          <Show when={props.grant.expires_at}>
            {(expires) => (
              <small data-testid="saved-approvals-expiry">
                {props.grant.expired
                  ? SAVED_APPROVALS_COPY.expiredBadge.toLowerCase()
                  : "expires"}{" "}
                {formatGrantExpiry(expires(), props.nowMs)}
              </small>
            )}
          </Show>
        </span>
        <DenButton
          variant="danger"
          compact
          data-testid="saved-approvals-revoke"
          disabled={props.actionsDisabled}
          onClick={() => props.onRevoke(props.grant)}
        >
          {props.grant.expired
            ? SAVED_APPROVALS_COPY.clearButton
            : SAVED_APPROVALS_COPY.revokeButton}
        </DenButton>
      </div>
    </li>
  );
}

export function SavedApprovalReviewGrantRow(props: {
  grant: ApprovalGrant;
  nowMs: number;
  actionsDisabled: boolean;
  onRevoke: (grant: ApprovalGrant) => void;
}) {
  return (
    <li
      class="den-settings-pref-row den-saved-approvals-priority-row"
      classList={{ "den-saved-approvals-row--expired": props.grant.expired === true }}
      data-testid="saved-approvals-row"
      data-grant-id={props.grant.id}
      data-grant-category={props.grant.category}
    >
      <div class="den-saved-approvals-priority-content">
        <span class="den-saved-approvals-priority-title">
          {props.grant.title} {renderRowBadges(props.grant)}
        </span>
        <span class="den-saved-approvals-priority-meta">
          {SAVED_APPROVALS_COPY.scopeLabel(
            props.grant.scope,
            !!props.grant.project_id && props.grant.scope === "device",
          )}
          <span aria-hidden="true">·</span>
          {formatRowDetail(props.grant) || GRANT_KIND_LABELS[props.grant.category]}
        </span>
        <Show
          when={
            props.grant.pattern &&
            props.grant.category !== "action_set" &&
            props.grant.category !== "host_resource"
          }
        >
          <code class="den-saved-approvals-priority-pattern">
            {props.grant.secret_names?.join(", ") || props.grant.pattern}
          </code>
        </Show>
        <Show when={(props.grant.secret_recipients?.length ?? 0) > 0}>
          <span class="den-saved-approvals-priority-detail" data-testid="saved-secret-recipients">
            {props.grant.secret_recipients?.map((recipient) => recipient.label).join("; ")}
          </span>
        </Show>
        <Show when={props.grant.expires_at}>
          {(expires) => (
            <span class="den-saved-approvals-priority-detail" data-testid="saved-approvals-expiry">
              {props.grant.expired ? SAVED_APPROVALS_COPY.expiredBadge.toLowerCase() : "expires"}{" "}
              {formatGrantExpiry(expires(), props.nowMs)}
            </span>
          )}
        </Show>
      </div>
      <DenButton
        variant="secondary"
        data-testid="saved-approvals-revoke"
        disabled={props.actionsDisabled}
        onClick={() => props.onRevoke(props.grant)}
      >
        {props.grant.expired
          ? SAVED_APPROVALS_COPY.clearButton
          : SAVED_APPROVALS_COPY.revokeButton}
      </DenButton>
    </li>
  );
}

export function SavedApprovalElevatedSection(props: {
  elevatedGrants: readonly ApprovalGrant[];
  elevatedQuiets: readonly AskQuiet[];
  sessionId?: string;
  applicableCount: number;
  summaryError: boolean;
  actionsDisabled: boolean;
  nowMs: number;
  onRevokeElevated: () => void;
  onRevokeGrant: (grant: ApprovalGrant) => void;
  onRevokeQuiet: (quiet: AskQuiet) => void;
}) {
  return (
    <Show when={props.elevatedGrants.length > 0 || props.elevatedQuiets.length > 0}>
      <section class="den-saved-approvals-band" data-testid="saved-approvals-band-elevated">
        <div class="den-saved-approvals-band-head den-saved-approvals-band-head--elevated">
          <h3>
            {APPROVALS_COPY.elevated.label}{" "}
            <span class="den-saved-approvals-priority-count">
              {props.elevatedGrants.length + props.elevatedQuiets.length}
            </span>
          </h3>
          <Show when={props.sessionId && props.applicableCount > 0 && !props.summaryError}>
            <DenButton
              variant="secondary"
              compact
              disabled={props.actionsDisabled}
              data-testid="saved-approvals-revoke-elevated"
              onClick={props.onRevokeElevated}
            >
              {SAVED_APPROVALS_COPY.revokeElevatedAll}
            </DenButton>
          </Show>
        </div>
        <p class="den-saved-approvals-priority-hint">{SAVED_APPROVALS_COPY.elevatedHint}</p>
        <ul
          class="den-settings-pref-group den-saved-approvals-priority-list"
          data-testid="saved-approvals-elevated-list"
        >
          <For each={props.elevatedGrants}>
            {(grant) => (
              <SavedApprovalReviewGrantRow
                grant={grant}
                nowMs={props.nowMs}
                actionsDisabled={props.actionsDisabled}
                onRevoke={props.onRevokeGrant}
              />
            )}
          </For>
          <For each={props.elevatedQuiets}>
            {(quiet) => (
              <li
                class="den-settings-pref-row den-saved-approvals-priority-row"
                data-testid="saved-approvals-quiet-row"
                data-quiet-id={quiet.id}
              >
                <span class="den-saved-approvals-priority-content">
                  <span class="den-saved-approvals-priority-title">{quiet.label}</span>
                  <span class="den-saved-approvals-priority-meta">
                    {SAVED_APPROVALS_COPY.scopeLabel("chat", false)}{" "}
                    <span aria-hidden="true">·</span>{" "}
                    {SAVED_APPROVALS_COPY.quietedGroupHeading}
                  </span>
                  <span class="den-saved-approvals-priority-detail">
                    {quiet.expires_at
                      ? SAVED_APPROVALS_COPY.quietedUntil(formatQuietExpiry(quiet, props.nowMs))
                      : SAVED_APPROVALS_COPY.quietedChat}{" "}
                    · {SAVED_APPROVALS_COPY.quietedSuppressed(quiet.suppressed)}
                  </span>
                </span>
                <DenButton
                  variant="secondary"
                  data-testid="saved-approvals-revoke-quiet"
                  disabled={props.actionsDisabled}
                  onClick={() => props.onRevokeQuiet(quiet)}
                >
                  {SAVED_APPROVALS_COPY.revokeButton}
                </DenButton>
              </li>
            )}
          </For>
        </ul>
      </section>
    </Show>
  );
}

export function SavedApprovalSocketCreateSection(props: {
  socketPath: string;
  onSocketPathInput: (value: string) => void;
  actionsDisabled: boolean;
  resolveBusy: boolean;
  createBusy: boolean;
  onResolveSocket: () => void;
  onCloseCreate: () => void;
  resolved: ResolveSocketGrantResponse | undefined;
  socketScope: ApprovalGrantScope;
  onSocketScopeChange: (scope: ApprovalGrantScope) => void;
  projects: readonly Project[];
  projectId: string;
  onProjectIdChange: (id: string) => void;
  onCreateSocketGrant: () => void;
}) {
  return (
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
          value={props.socketPath}
          onInput={(e) => props.onSocketPathInput(e.currentTarget.value)}
        />
      </label>
      <div class="den-saved-approvals-create-actions">
        <DenButton
          variant="secondary"
          data-testid="saved-approvals-socket-resolve"
          disabled={props.actionsDisabled || props.resolveBusy || !props.socketPath.trim()}
          onClick={props.onResolveSocket}
        >
          {SAVED_APPROVALS_COPY.socketResolve}
        </DenButton>
        <DenButton
          variant="secondary"
          data-testid="saved-approvals-socket-cancel"
          disabled={props.createBusy}
          onClick={props.onCloseCreate}
        >
          {SAVED_APPROVALS_COPY.socketCancel}
        </DenButton>
      </div>
      <Show when={props.resolved} keyed>
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
              value={props.socketScope}
              onChange={(id) => props.onSocketScopeChange(id as ApprovalGrantScope)}
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
            <Show when={props.socketScope === "project"}>
              <label class="den-settings-field">
                <span>{SAVED_APPROVALS_COPY.socketProjectLabel}</span>
                <Show
                  when={props.projects.length > 0}
                  fallback={
                    <p class="den-settings-hint">
                      {SAVED_APPROVALS_COPY.socketProjectMissing}
                    </p>
                  }
                >
                  <DenSelect
                    aria-label="Project"
                    data-testid="saved-approvals-socket-project"
                    value={props.projectId}
                    options={props.projects.map((project) => ({
                      value: project.id,
                      label: projectName(project),
                    }))}
                    onValueChange={props.onProjectIdChange}
                  />
                </Show>
              </label>
            </Show>
            <div class="den-saved-approvals-create-actions">
              <DenButton
                variant="primary"
                data-testid="saved-approvals-socket-confirm-create"
                disabled={props.actionsDisabled}
                onClick={props.onCreateSocketGrant}
              >
                {SAVED_APPROVALS_COPY.socketConfirm}
              </DenButton>
            </div>
          </div>
        )}
      </Show>
    </section>
  );
}

export function SavedApprovalGroupsSection(props: {
  projectId?: string;
  chatGroups: readonly Extract<GrantScopeGroup, { kind: "chat" }>[];
  projectGroups: readonly Extract<GrantScopeGroup, { kind: "project" }>[];
  deviceGroup: Extract<GrantScopeGroup, { kind: "device" }> | undefined;
  quietGroups: readonly QuietScopeGroup[];
  nowMs: number;
  actionsDisabled: boolean;
  onRevokeGrant: (grant: ApprovalGrant) => void;
  onRevokeGroup: (group: GrantScopeGroup) => void;
  onRevokeQuiet: (quiet: AskQuiet) => void;
}) {
  const renderGroupRows = (group: GrantScopeGroup) => (
    <ul class="den-saved-approvals-list" data-testid="saved-approvals-table">
      <For each={group.grants}>
        {(grant) => (
          <Show
            when={props.projectId}
            fallback={
              <SavedApprovalGrantRow
                grant={grant}
                nowMs={props.nowMs}
                actionsDisabled={props.actionsDisabled}
                onRevoke={props.onRevokeGrant}
              />
            }
          >
            <SavedApprovalReviewGrantRow
              grant={grant}
              nowMs={props.nowMs}
              actionsDisabled={props.actionsDisabled}
              onRevoke={props.onRevokeGrant}
            />
          </Show>
        )}
      </For>
    </ul>
  );

  return (
    <>
      <Show when={props.chatGroups.length > 0}>
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
          <For each={props.chatGroups}>
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
                    disabled={props.actionsDisabled}
                    onClick={() => props.onRevokeGroup(group)}
                  >
                    {SAVED_APPROVALS_COPY.revokeGroupChat}
                  </button>
                </div>
                {renderGroupRows(group)}
              </div>
            )}
          </For>
        </section>
      </Show>

      <For each={props.projectGroups}>
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
                disabled={props.actionsDisabled}
                onClick={() => props.onRevokeGroup(group)}
              >
                {SAVED_APPROVALS_COPY.revokeGroupProject}
              </button>
            </div>
            <div class="den-saved-approvals-card">{renderGroupRows(group)}</div>
          </section>
        )}
      </For>

      <Show when={props.deviceGroup} keyed>
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
                disabled={props.actionsDisabled}
                onClick={() => props.onRevokeGroup(group)}
              >
                {SAVED_APPROVALS_COPY.revokeGroupDevice}
              </button>
            </div>
            <div class="den-saved-approvals-card">{renderGroupRows(group)}</div>
          </section>
        )}
      </Show>

      <Show when={props.quietGroups.length > 0}>
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
          <For each={props.quietGroups}>
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
                                  formatQuietExpiry(quiet, props.nowMs),
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
                          disabled={props.actionsDisabled}
                          onClick={() => props.onRevokeQuiet(quiet)}
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
    </>
  );
}
