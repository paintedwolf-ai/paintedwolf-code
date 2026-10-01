import { useResidentPresence } from "../../ui/resident-presence-context.tsx";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { managedSecretId } from "../../api/managed-secret-reference.ts";
import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  untrack,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ManagedSecret, ManagedSecretUse } from "../../api/types.ts";
import { InlineNotice } from "../../notices/InlineNotice.tsx";
import {
  noticeFromCaught,
  noticeFromWire,
  type AppNotice,
} from "../../notices/notice-model.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";
import {
  ManagedSecretRevealError,
  revealManagedSecret,
} from "../../platform/files/managed-secret-reveal.ts";
import { copyTextToClipboard, writeClipboardText } from "../../utils/clipboard.ts";
import { paginateSlice } from "../../list/pagination.ts";
import {
  MANAGED_SECRETS_COPY as C,
  secretRowSummary,
  secretStateLabel,
  originFilterLabel,
  stateFilterLabel,
} from "../../settings/security/managed-secrets-copy.ts";
import {
  DEFAULT_SECRET_FILTER,
  filterSecrets,
  hiddenRevokedCount,
  sortSecretsByNewest,
  type SecretFilter,
  type SecretOriginFilter,
  type SecretStateFilter,
} from "../../settings/security/managed-secrets-model.ts";
import { ListSurfaceInbox } from "../list/ListSurfaceInbox.tsx";
import { TablePager } from "../list/TablePager.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { SettingsListBackChrome } from "../settings/SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../settings/SettingsListChrome.tsx";
import { SettingsListGroup } from "../settings/SettingsListGroup.tsx";
import { SettingsListPanel } from "../settings/SettingsListPanel.tsx";
import { SettingsListRow } from "../settings/SettingsListRow.tsx";
import { ManagedSecretDetail, type DetailMode } from "./ManagedSecretDetail.tsx";
import {
  ManagedSecretAddForm,
  ManagedSecretEditForm,
  ManagedSecretAgentUseDeadlineForm,
  ManagedSecretValueForm,
} from "./ManagedSecretForms.tsx";

type Props = {
  client: LycaonClient;
  projectId: string;
};

export const SECRETS_PAGE_SIZE = 10;

const COPIED_MS = 1400;

const STATE_FILTERS: SecretStateFilter[] = ["live", "all", "revoked"];
const ORIGIN_FILTERS: SecretOriginFilter[] = [
  "all",
  "generated",
  "settings_entered",
  "ask_user_response",
  "file_marked",
  "composer_marked",
  "detected",
  "cookie_jar",
  "token_jar",
];

export function ManagedSecretsPanel(props: Props) {
  const [items, setItems] = createSignal<ManagedSecret[]>([]);
  const [loading, setLoading] = createSignal(true);
  usePresentationParticipant("managed-secrets", () => !loading());
  const [refreshing, setRefreshing] = createSignal(false);
  const [selectedId, setSelectedId] = createSignal<string>();
  const [adding, setAdding] = createSignal(false);
  const [mode, setMode] = createSignal<DetailMode>("facts");
  const [busy, setBusy] = createSignal(false);
  const [copied, setCopied] = createSignal(false);
  const [page, setPage] = createSignal(0);
  const [filter, setFilter] = createSignal<SecretFilter>(DEFAULT_SECRET_FILTER);
  const [error, setError] = createSignal<AppNotice>();
  const [note, setNote] = createSignal<string>();
  const [uses, setUses] = createSignal<ManagedSecretUse[]>([]);
  const [usesLoading, setUsesLoading] = createSignal(false);
  const [usesFailed, setUsesFailed] = createSignal(false);
  const [revealed, setRevealed] = createSignal<{
    secretId: string;
    value: string;
    deadline: number;
  }>();
  const [revealBusy, setRevealBusy] = createSignal(false);
  const [revealSeconds, setRevealSeconds] = createSignal(0);
  const [revealCopied, setRevealCopied] = createSignal(false);
  const [revealCopyFailed, setRevealCopyFailed] = createSignal(false);

  let alive = true;
  let listRequest = 0;
  let usesRequest = 0;
  let revealRequest = 0;
  let copiedTimer: ReturnType<typeof setTimeout> | undefined;
  let revealTimer: ReturnType<typeof setInterval> | undefined;

  const clearRevealTimer = () => {
    clearInterval(revealTimer);
    revealTimer = undefined;
  };

  const clearReveal = () => {
    clearRevealTimer();
    setRevealed(undefined);
    setRevealSeconds(0);
    setRevealCopied(false);
    setRevealCopyFailed(false);
  };

  const hideReveal = () => {
    revealRequest++;
    setRevealBusy(false);
    clearReveal();
  };

  const presence = useResidentPresence();
  createEffect(() => { if (presence() !== "active") hideReveal(); });
  // Native authentication can take focus, so blur preserves the pending reveal request.
  const hideOnBlur = () => clearReveal();
  const hideWhenHidden = () => {
    if (document.visibilityState !== "visible") hideReveal();
  };
  if (typeof window !== "undefined") {
    window.addEventListener("blur", hideOnBlur);
    document.addEventListener("visibilitychange", hideWhenHidden);
  }
  onCleanup(() => {
    alive = false;
    clearTimeout(copiedTimer);
    hideReveal();
    if (typeof window !== "undefined") {
      window.removeEventListener("blur", hideOnBlur);
      document.removeEventListener("visibilitychange", hideWhenHidden);
    }
  });

  const catchNotice = (err: unknown, fallback: string) =>
    noticeFromCaught(err, APP_SCOPE, { title: fallback, message: fallback });

  const load = async (refresh: boolean) => {
    const request = ++listRequest;
    const client = props.client;
    const projectId = props.projectId;
    const current = () => alive && request === listRequest && client === props.client && projectId === props.projectId;
    if (refresh) setRefreshing(true);
    else setLoading(true);
    setError(undefined);
    try {
      const next = await client.listProjectManagedSecrets(projectId);
      if (current()) setItems(next.secrets);
    } catch (err) {
      if (current()) setError(catchNotice(err, C.loadError));
    } finally {
      if (!current()) return;
      setLoading(false);
      setRefreshing(false);
    }
  };

  createEffect(() => {
    props.client;
    props.projectId;
    untrack(hideReveal);
    setSelectedId(undefined);
    usesRequest++;
    setUses([]);
    setItems([]);
    setAdding(false);
    setPage(0);
    setFilter(DEFAULT_SECRET_FILTER);
    void load(false);
  });

  const sorted = createMemo(() => sortSecretsByNewest(items()));
  const visible = createMemo(() => filterSecrets(sorted(), filter()));
  const hiddenRevoked = createMemo(() => hiddenRevokedCount(sorted(), filter()));
  const paged = createMemo(() => paginateSlice(visible(), page(), SECRETS_PAGE_SIZE));

  createEffect(() => {
    const clamped = paged().page;
    if (clamped !== page()) setPage(clamped);
  });

  const groups = createMemo(() => {
    const slice = paged().slice;
    return [
      { key: "project" as const, label: C.groupProject },
      { key: "chat" as const, label: C.groupChat },
    ]
      .map((group) => ({
        ...group,
        entries: slice.filter((secret) => secret.scope === group.key),
      }))
      .filter((group) => group.entries.length > 0);
  });

  const grouped = () => groups().length > 1;

  const selected = createMemo(() =>
    sorted().find((secret) => managedSecretId(secret.reference) === selectedId()),
  );

  const detailOpen = () => adding() || selected() != null;

  const patchFilter = (patch: Partial<SecretFilter>) => {
    setFilter({ ...filter(), ...patch });
    setPage(0);
  };

  const loadUses = async (id: string) => {
    const request = ++usesRequest;
    const client = props.client;
    const projectId = props.projectId;
    const current = () => alive && request === usesRequest && selectedId() === id && client === props.client && projectId === props.projectId;
    setUses([]);
    setUsesFailed(false);
    setUsesLoading(true);
    try {
      const history = await client.listProjectManagedSecretUses(
        projectId,
        id,
      );
      if (current()) setUses(history.uses);
    } catch {
      if (current()) setUsesFailed(true);
    } finally {
      if (current()) setUsesLoading(false);
    }
  };

  const openDetail = (secret: ManagedSecret) => {
    const id = managedSecretId(secret.reference);
    if (!id) {
      setError(catchNotice(undefined, C.invalidReference));
      return;
    }
    setCopied(false);
    hideReveal();
    setNote(undefined);
    setError(undefined);
    setAdding(false);
    setMode("facts");
    setSelectedId(id);
    void load(true);
    void loadUses(id);
  };

  let wasActive = presence() === "active";
  createEffect(() => {
    const active = presence() === "active";
    if (active && !wasActive) untrack(() => {
      if (adding() || mode() !== "facts") return;
      void load(true);
      const id = selectedId();
      if (id) void loadUses(id);
    });
    wasActive = active;
  });

  const closeDetail = () => {
    hideReveal();
    setSelectedId(undefined);
    setAdding(false);
    setMode("facts");
    setNote(undefined);
  };

  const copyReference = (secret: ManagedSecret) => {
    void copyTextToClipboard(secret.reference);
    setCopied(true);
    clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => {
      if (alive) setCopied(false);
    }, COPIED_MS);
  };

  const absorb = (updated: ManagedSecret) => {
    setItems((current) =>
      current.map((item) =>
        item.reference === updated.reference ? updated : item,
      ),
    );
  };

  const reveal = async (secret: ManagedSecret, id: string) => {
    hideReveal();
    const request = revealRequest;
    const projectId = props.projectId;
    const current = () => alive && request === revealRequest && projectId === props.projectId
      && presence() === "active" && selectedId() === id;
    setRevealBusy(true);
    setError(undefined);
    try {
      const result = await revealManagedSecret(projectId, id);
      if (!current() || document.visibilityState !== "visible" || !document.hasFocus()) return;
      const seconds = Math.max(5, Math.min(60, Math.round(result.remask_after_ms / 1000)));
      const deadline = Date.now() + seconds * 1000;
      setRevealed({ secretId: id, value: result.secret_value, deadline });
      setRevealSeconds(seconds);
      revealTimer = setInterval(() => {
        const current = revealed();
        if (!current || current.secretId !== id) {
          clearRevealTimer();
          return;
        }
        const remaining = Math.max(
          0,
          Math.ceil((current.deadline - Date.now()) / 1000),
        );
        setRevealSeconds(remaining);
        if (remaining === 0) hideReveal();
      }, 1000);
      absorb({
        ...secret,
        last_revealed_at: result.revealed_at,
        reveal_count: secret.reveal_count + 1,
      });
    } catch (err) {
      if (!current()) return;
      if (err instanceof ManagedSecretRevealError) {
        setError(
          noticeFromWire(
            {
              code: err.code,
              title: err.title ?? C.revealError,
              message: err.message,
              suggested_action: err.suggestedAction,
              actions: err.actions,
              scope: err.scope,
              resolution: err.resolution,
            },
            APP_SCOPE,
          ),
        );
      } else {
        setError(catchNotice(undefined, C.revealError));
      }
    } finally {
      if (current()) setRevealBusy(false);
    }
  };

  const copyRevealedValue = (selection?: string) => {
    const current = revealed();
    if (!current) return;
    if (current.deadline <= Date.now() || document.visibilityState !== "visible" || !document.hasFocus()) {
      hideReveal();
      return;
    }
    setRevealCopyFailed(false);
    // A copy event already put the selection on the clipboard.
    if (selection) {
      setRevealCopied(true);
      return;
    }
    setRevealCopied(false);
    void writeClipboardText(current.value).then(
      () => {
        if (alive && revealed() === current) {
          setRevealCopied(true);
        }
      },
      () => {
        if (alive && revealed() === current) {
          setRevealCopyFailed(true);
        }
      },
    );
  };

  const changeMode = (next: DetailMode) => {
    hideReveal();
    setMode(next);
  };

  const mutate = async (
    fallback: string,
    run: () => Promise<ManagedSecret>,
  ): Promise<boolean> => {
    setBusy(true);
    setError(undefined);
    try {
      const updated = await run();
      if (!alive) return false;
      absorb(updated);
      setMode("facts");
      return true;
    } catch (err) {
      if (alive) setError(catchNotice(err, fallback));
      return false;
    } finally {
      if (alive) setBusy(false);
    }
  };

  const addSecret = async (draft: {
    name: string;
    purpose: string;
    value: string;
    agentUseEndsAt: string;
  }) => {
    setBusy(true);
    setError(undefined);
    setNote(undefined);
    try {
      const created = await props.client.createProjectManagedSecret(props.projectId, {
        operation_id: crypto.randomUUID(),
        name: draft.name.trim(),
        purpose: draft.purpose.trim(),
        secret_value: draft.value,
        ...(draft.agentUseEndsAt
          ? { agent_use_ends_at: draft.agentUseEndsAt }
          : {}),
      });
      if (!alive) return;
      // Reused values return their existing capability.
      const existing = items().some((item) => item.reference === created.reference);
      setItems((current) =>
        existing
          ? current.map((item) =>
              item.reference === created.reference ? created : item,
            )
          : [created, ...current],
      );
      setAdding(false);
      setMode("facts");
      const id = managedSecretId(created.reference);
      setSelectedId(id ?? undefined);
      setNote(existing ? C.addedExisting(created.name) : undefined);
      if (id) void loadUses(id);
    } catch (err) {
      if (alive) setError(catchNotice(err, C.addError));
    } finally {
      if (alive) setBusy(false);
    }
  };

  const revoke = async (secret: ManagedSecret) => {
    hideReveal();
    const client = props.client;
    const projectId = props.projectId;
    const current = () => alive && client === props.client && projectId === props.projectId;
    const id = managedSecretId(secret.reference);
    if (!id) {
      setError(catchNotice(undefined, C.invalidReference));
      return;
    }
    const confirmed = await confirmDestructive({
      title: C.revokeConfirmTitle,
      message: C.revokeConfirmMessage(secret.name),
      okLabel: C.revokeConfirmOk,
    });
    if (!confirmed || !current()) return;
    setBusy(true);
    setError(undefined);
    try {
      await client.revokeProjectManagedSecret(projectId, id);
      if (!current()) return;
      await load(true);
      if (current()) setMode("facts");
    } catch (err) {
      if (current()) setError(catchNotice(err, C.revokeError));
    } finally {
      if (alive) setBusy(false);
    }
  };

  const renderRow = (secret: ManagedSecret) => {
    const id = managedSecretId(secret.reference);
    return (
      <SettingsListRow
        testId={`managed-secret-${id ?? "invalid"}`}
        selected={id != null && id === selectedId()}
        variant={
          secret.state === "unavailable" || secret.state === "agent_use_expired"
            ? "warning"
            : "default"
        }
        primary={secret.name}
        secondary={secretRowSummary(secret)}
        status={
          <span
            class="den-settings-provider-status"
            data-configured={secret.state === "active"}
          >
            {secretStateLabel(secret)}
          </span>
        }
        onSelect={() => openDetail(secret)}
      />
    );
  };

  const detailForm = (secret: ManagedSecret, id: string) => {
    switch (mode()) {
      case "edit":
        return (
          <ManagedSecretEditForm
            name={secret.name}
            purpose={secret.purpose ?? ""}
            busy={busy()}
            onCancel={() => setMode("facts")}
            onSubmit={(patch) =>
              void mutate(C.editError, () =>
                props.client.updateProjectManagedSecret(props.projectId, id, patch),
              )
            }
          />
        );
      case "value":
        return (
          <ManagedSecretValueForm
            restoring={secret.state === "unavailable"}
            markedInFile={secret.origin === "file_marked"}
            busy={busy()}
            onCancel={() => setMode("facts")}
            onSubmit={(value) =>
              void mutate(C.replaceError, () =>
                props.client.replaceProjectManagedSecretValue(props.projectId, id, {
                  secret_value: value,
                }),
              )
            }
          />
        );
      case "agent_use_deadline":
        return (
          <ManagedSecretAgentUseDeadlineForm
            busy={busy()}
            onCancel={() => setMode("facts")}
            onSubmit={(at) =>
              void mutate(C.agentUseDeadlineError, () =>
                props.client.updateProjectManagedSecret(props.projectId, id, {
                  agent_use_ends_at: at ? at : null,
                }),
              )
            }
          />
        );
      default:
        return null;
    }
  };

  const listChrome = () => (
    <SettingsListChrome
      testId="managed-secrets-list-chrome"
      count={
        <span data-testid="managed-secrets-count">
          {loading() ? C.loadingCount : C.countLabel(visible().length)}
          <Show when={hiddenRevoked() > 0}>
            {" · "}
            <button
              type="button"
              class="den-settings-inline-toggle"
              data-testid="managed-secrets-show-revoked"
              onClick={() => patchFilter({ state: "all" })}
            >
              {C.hiddenRevoked(hiddenRevoked())}
            </button>
          </Show>
        </span>
      }
      secondaryAction={
        <DenButton
          variant="secondary"
          compact
          disabled={loading() || refreshing()}
          data-testid="managed-secrets-refresh"
          onClick={() => void load(true)}
        >
          {refreshing() ? C.refreshing : C.refresh}
        </DenButton>
      }
      action={
        <DenButton
          variant="primary"
          compact
          data-testid="managed-secrets-add"
          onClick={() => {
            setSelectedId(undefined);
            setNote(undefined);
            setError(undefined);
            setAdding(true);
          }}
        >
          {C.add}
        </DenButton>
      }
    />
  );

  return (
    <div
      class="den-settings-section"
      data-testid="managed-secrets-panel"
    >
      <p class="den-settings-hint">{C.lede}</p>

      <InlineNotice notice={error()} testId="managed-secrets-error" />
      <Show when={note()} keyed>
        {(message) => (
          <p class="den-settings-hint" role="status" data-testid="managed-secrets-note">
            {message}
          </p>
        )}
      </Show>

      <SettingsListPanel
        class="den-managed-secrets-panel"
        testId="managed-secrets-list-panel"
        chrome={
          detailOpen() ? (
            <SettingsListBackChrome
              label={C.back}
              testId="managed-secrets-back"
              onBack={closeDetail}
            />
          ) : (
            listChrome()
          )
        }
      >
        <Show when={!detailOpen()}>
          <div class="den-secret-filters" data-testid="managed-secrets-filters">
            <DenSelect
              aria-label={C.filterState}
              data-testid="managed-secrets-filter-state"
              value={filter().state}
              options={STATE_FILTERS.map((state) => ({
                value: state,
                label: stateFilterLabel(state),
              }))}
              onValueChange={(value) =>
                patchFilter({ state: value as SecretStateFilter })
              }
            />
            <DenSelect
              aria-label={C.filterOrigin}
              data-testid="managed-secrets-filter-origin"
              value={filter().origin}
              options={ORIGIN_FILTERS.map((origin) => ({
                value: origin,
                label: originFilterLabel(origin),
              }))}
              onValueChange={(value) =>
                patchFilter({ origin: value as SecretOriginFilter })
              }
            />
            <DenInput
              aria-label={C.filterQuery}
              data-testid="managed-secrets-filter-query"
              placeholder={C.filterQueryPlaceholder}
              value={filter().query}
              onInput={(event) => patchFilter({ query: event.currentTarget.value })}
            />
          </div>
        </Show>

        <ListSurfaceInbox
          detailOpen={detailOpen()}
          scrollport
          isEmpty={!loading() && visible().length === 0}
          listTestId="managed-secrets-list"
          detailTestId="managed-secrets-detail"
          empty={
            <p
              class="den-settings-hint den-settings-list-inbox__empty"
              data-testid="managed-secrets-empty"
            >
              {items().length === 0 ? C.empty : C.emptyFiltered}
            </p>
          }
          list={
            <>
              <Show when={loading() && items().length === 0}>
                <p
                  class="den-settings-hint den-settings-list-inbox__empty"
                  data-testid="managed-secrets-loading"
                  role="status"
                >
                  {C.loading}
                </p>
              </Show>
              <For each={groups()}>
                {(group) => (
                  <Show
                    when={grouped()}
                    fallback={
                      <div data-testid={`managed-secrets-group-${group.key}`}>
                        <For each={group.entries}>{(s) => renderRow(s)}</For>
                      </div>
                    }
                  >
                    <SettingsListGroup
                      label={group.label}
                      meta={C.countLabel(group.entries.length)}
                      testId={`managed-secrets-group-${group.key}`}
                    >
                      <For each={group.entries}>{(s) => renderRow(s)}</For>
                    </SettingsListGroup>
                  </Show>
                )}
              </For>
              <TablePager
                page={page()}
                pageSize={SECRETS_PAGE_SIZE}
                total={paged().total}
                onPageChange={setPage}
                testId="managed-secrets-pager"
                ariaLabel={`${C.listAriaLabel} pagination`}
                compact
              />
            </>
          }
          detail={
            <Show
              when={!adding()}
              fallback={
                <ManagedSecretAddForm
                  busy={busy()}
                  onCancel={closeDetail}
                  onSubmit={(draft) => void addSecret(draft)}
                />
              }
            >
              <Show when={selected()} keyed>
                {(secret) => {
                  const id = managedSecretId(secret.reference) ?? "";
                  return (
                    <ManagedSecretDetail
                      secret={secret}
                      secretId={id}
                      mode={mode()}
                      copied={copied()}
                      busy={busy()}
                      revealBusy={revealBusy()}
                      revealedValue={
                        revealed()?.secretId === id ? revealed()?.value : undefined
                      }
                      revealSeconds={revealSeconds()}
                      revealCopied={revealCopied()}
                      revealCopyFailed={revealCopyFailed()}
                      uses={{
                        items: uses(),
                        loading: usesLoading(),
                        failed: usesFailed(),
                      }}
                      form={detailForm(secret, id)}
                      onCopyReference={() => copyReference(secret)}
                      onReveal={() => void reveal(secret, id)}
                      onHideReveal={hideReveal}
                      onCopyValue={copyRevealedValue}
                      onMode={changeMode}
                      onPromote={() =>
                        void mutate(C.promoteError, () =>
                          props.client.updateProjectManagedSecret(props.projectId, id, {
                            scope: "project",
                          }),
                        )
                      }
                      onRevoke={() => void revoke(secret)}
                    />
                  );
                }}
              </Show>
            </Show>
          }
        />
      </SettingsListPanel>
    </div>
  );
}
