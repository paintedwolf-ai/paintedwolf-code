import { createMcpProviderTools } from "./mcp-provider-tools.ts";
import { createMcpConnectionEditor } from "./mcp-connection-editor.ts";
import { createMcpAddProvider } from "./mcp-add-provider.ts";
import { createMcpProviderActions, projectDeviceManaged } from "./mcp-provider-actions.ts";
import { createMcpSignIn } from "./mcp-sign-in.ts";
import { settingControl, settingLabel } from "../../../settings/settings-registry.ts";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { useResidentLive } from "../../../ui/resident-activity.ts";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { For, Show, createEffect, createMemo, createSignal, on, type Accessor } from "solid-js";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import { SettingsGovernedGroup } from "../SettingsGovernedGroup.tsx";
import type { LycaonClient } from "../../../api/client.ts";
import type { McpProvider, McpToolInfo } from "../../../api/types.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { settledEmpty, unloaded, valueOf } from "../../../store/load-state.ts";
import { noticeFromCaught, noticeFromCopy, type AppNotice } from "../../../notices/notice-model.ts";
import { APP_SCOPE, projectScope } from "../../../notices/notice-scope.ts";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import { catalogProviders, groupProvidersByClass, groupRecipesByClass, isRejectedProvider, providersForScope, providerLocksCredentialWire, providerOffersSignIn } from "../../../settings/mcp/mcp-provider-model.ts";
import { MCP_SETTINGS_COPY } from "../../../settings/mcp/mcp-settings-copy.ts";

import { confirmAndOpenExternalLink } from "../../../platform/desktop/external-link.ts";

import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../../settings/security/project-settings-overlay-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { SettingsEditorTitle, McpIcon } from "../SettingsEditorTitle.tsx";
import { ProjectSettingsOverrideControl } from "../ProjectSettingsOverrideControl.tsx";
import { SettingsScopeLede } from "../SettingsScopeLede.tsx";
import { SettingsListBackChrome } from "../SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../SettingsListChrome.tsx";
import { SettingsListGroup } from "../SettingsListGroup.tsx";
import { ListSurfaceInbox } from "../../list/ListSurfaceInbox.tsx";
import { SettingsListPanel } from "../SettingsListPanel.tsx";
import { SettingsListRow } from "../SettingsListRow.tsx";
import { emptyAddDraft, draftFromProvider } from "./mcp-connection-draft.ts";
import { McpConnectionFields } from "./McpConnectionFields.tsx";

type Props = {
  client: LycaonClient;
  /** Project-scoped provider configuration. */
  projectId?: string;
  alwaysProjectScope?: boolean;
  /** Opens the counterpart settings surface. */
  counterpartLabel?: string;
  onOpenCounterpart?: () => void;
};

/** What the panel holds and hands to its controllers; each declares the members it reads as a `Pick`. */
export type McpSettingsScope = {
  currentClient: () => LycaonClient;
  /** Set only for a project-local panel. */
  projectId: () => string | undefined;
  projectLocal: () => boolean;
  live: Accessor<boolean>;
  providers: () => McpProvider[];
  /** Captures the provider list's current query target for a later write. */
  providerWriter: () => (next: McpProvider[] | ((previous: McpProvider[]) => McpProvider[])) => void;
  reloadProviders: () => Promise<unknown>;
  catchNotice: (error: unknown) => AppNotice;
  setOperationError: (notice: AppNotice | undefined) => void;
};

function providerIsConfigured(provider: McpProvider): boolean {
  if (isRejectedProvider(provider)) return false;
  if (provider.status === "ready") return true;
  if (provider.status === "needs_auth" || provider.status === "error") return false;
  return Boolean(provider.enabled) && !provider.last_error;
}

function providerCountLabel(n: number): string {
  return n === 1 ? "1 provider" : `${n} providers`;
}

export function McpSettingsPanel(props: Props) {
  const live = useResidentLive();
  const providerQuery = createSurfaceQuery({
    name: "mcp-providers",
    source: () => ({ client: props.client, key: props.alwaysProjectScope ? props.projectId ?? "global" : "global", projectId: props.alwaysProjectScope ? props.projectId : undefined }),
    load: ({ client, projectId }) => client.listMcpProviders(projectId),
  });
  const providers = () => providerQuery.value() ?? [];
  const loading = providerQuery.loading;
  const [operationError, setOperationError] = createSignal<AppNotice | undefined>();
  const loadError = () => operationError() ?? (providerQuery.error() ? catchNotice(providerQuery.cause()) : undefined);
  const providerWriter = () => {
    const target = providerQuery.capture();
    return (next: McpProvider[] | ((previous: McpProvider[]) => McpProvider[])) =>
      target.update((previous) => typeof next === "function" ? next(previous ?? []) : next);
  };
  const scope: McpSettingsScope = {
    currentClient: () => props.client, projectId: () => projectId(), projectLocal: () => projectLocal(),
    live, providers, providerWriter, reloadProviders: () => providerQuery.refresh(),
    catchNotice: (error) => catchNotice(error), setOperationError,
  };
  const { checkRows, setCheckRows, checkRunning, checkError, togglingId, busyOverride,
    projectOverrideEnabled, observeOverride, toggleEnabled, toggleAlwaysLoad,
    applyProjectOverride, testConnection, deleteProvider } = createMcpProviderActions(scope);
  const [selectedProviderId, setSelectedProviderId] = createSignal<
    string | undefined
  >();
  const { addOpen, addStep, setAddStep, addCustomClass, setAddCustomClass, recipes,
    addDraft, setAddDraft, addError, setAddError, addBusy, addReady, openAdd, closeAdd, saveAdd,
    addRecipe } = createMcpAddProvider({ ...scope, selectProviderId: setSelectedProviderId });
  const { connectionDraft, setConnectionDraft, connectionBusy, connectionError,
    setConnectionError, saveConnection } = createMcpConnectionEditor(scope);
  const { toolsOpenId, setToolsOpenId, toolsById, toolsLoadingId, resyncingId,
    observeProviderTools, toggleTools, resyncProvider } = createMcpProviderTools(scope);
  const { oauthBusyId, oauthPending, oauthCode, setOauthCode, oauthError, oauthManual,
    setOauthManual, startSignIn, cancelSignIn, completeSignIn, signOut, observeSignIn,
  } = createMcpSignIn(scope);
  let addDialogRef: HTMLDivElement | undefined;
  let oauthDialogRef: HTMLDivElement | undefined;

  const projectLocal = () => props.alwaysProjectScope === true;
  const projectId = () =>
    projectLocal() ? props.projectId : undefined;
  const noticeScope = () =>
    projectLocal() && props.projectId
      ? projectScope(props.projectId)
      : APP_SCOPE;
  const catchNotice = (err: unknown) => noticeFromCaught(err, noticeScope());
  observeOverride();

  const projectCannotEnable = (provider: McpProvider) =>
    projectLocal() && !provider.enabled && projectDeviceManaged(provider);

  const projectCanEditConnection = (provider: McpProvider) =>
    !projectLocal() ||
    (provider.transport === "http" && !projectDeviceManaged(provider));

  const listGroups = createMemo(() =>
    groupProvidersByClass(providers(), projectLocal()),
  );

  const listProviders = createMemo(() =>
    listGroups().flatMap((g) => g.providers),
  );

  const recipeGroups = createMemo(() =>
    groupRecipesByClass(recipes(), projectLocal()),
  );

  const selectedProvider = createMemo(() => {
    const id = selectedProviderId();
    if (!id) return undefined;
    return listProviders().find((s) => s.id === id);
  });

  const followingSummary = () => {
    const list = providersForScope(
      catalogProviders(providers()),
      projectLocal(),
    );
    if (list.length === 0) return "no providers registered";
    const enabled = list.filter((s) => s.enabled).length;
    return MCP_SETTINGS_COPY.followingSummary(enabled, list.length);
  };

  const listVisible = () => !projectLocal() || projectOverrideEnabled();

  createEffect(() => {
    const list = listProviders();
    const cur = selectedProviderId();
    if (list.length === 0) {
      setSelectedProviderId(undefined);
      return;
    }
    if (cur && !list.some((s) => s.id === cur)) {
      setSelectedProviderId(undefined);
    }
  });

  createEffect(
    on(selectedProviderId, (id) => {
      if (!id) return;
      const provider = listProviders().find((s) => s.id === id);
      if (provider && !isRejectedProvider(provider)) {
        setConnectionDraft(draftFromProvider(provider));
        setConnectionError(undefined);
      }
    }),
  );

  createModalFocusTrap(addOpen, () => addDialogRef, {
    onEscape: closeAdd,
  });
  createModalFocusTrap(
    () => oauthPending() !== undefined,
    () => oauthDialogRef,
    {
      onEscape: () => {
        if (!oauthBusyId()) void cancelSignIn();
      },
    },
  );

  observeProviderTools();

  observeSignIn();

  const selectProvider = (provider: McpProvider) => {
    setSelectedProviderId(provider.id);
    setToolsOpenId(undefined);
  };

  const closeDetail = () => {
    setSelectedProviderId(undefined);
    setToolsOpenId(undefined);
  };

  const renderListRow = (provider: McpProvider) => {
    const rejected = isRejectedProvider(provider);

    return (
      <SettingsListRow
        testId={rejected ? "mcp-rejected-row" : `mcp-provider-${provider.id}`}
        variant={rejected ? "rejected" : "default"}
        primary={provider.id}
        secondary={
          !rejected && provider.transport ? (
            <span data-testid={`mcp-transport-${provider.id}`}>
              {provider.transport}
            </span>
          ) : undefined
        }
        status={
          rejected || provider.status ? (
            <span
              class="den-settings-provider-status"
              data-testid={`mcp-status-${provider.id}`}
              data-status={provider.status}
              data-configured={providerIsConfigured(provider)}
            >
              {rejected ? MCP_SETTINGS_COPY.rejectedLabel : provider.status}
            </span>
          ) : undefined
        }
        onSelect={() => selectProvider(provider)}
      />
    );
  };

  const renderRejectedDetail = (provider: Accessor<McpProvider>) => (
    <div
      class="den-settings-mcp-detail-wrap"
      data-testid="mcp-rejected-detail"
    >
      <article class="den-settings-provider-card">
        <header class="den-settings-provider-head">
          <strong>{provider().id}</strong>
          <span
            class="den-settings-provider-status"
            data-configured={false}
          >
            {MCP_SETTINGS_COPY.rejectedLabel}
          </span>
        </header>
        <InlineNotice
          notice={noticeFromCopy(
            provider().notice,
            provider().last_error,
            noticeScope(),
          )}
          testId="mcp-rejected-notice"
        />
        <Show when={provider().connection_source}>
          <p class="den-settings-hint">
            {MCP_SETTINGS_COPY.columnSource}: {provider().connection_source}
          </p>
        </Show>
      </article>
    </div>
  );

  const renderProviderDetail = (provider: Accessor<McpProvider>) => {
    const busy = () =>
      togglingId() === provider().id ||
      busyOverride() ||
      connectionBusy() ||
      addBusy();

    return (
      <div
        class="den-settings-mcp-detail-wrap"
        data-testid={`mcp-provider-detail-${provider().id}`}
      >
        <article class="den-settings-provider-card">
          <header class="den-settings-provider-head">
            <strong>{provider().id}</strong>
            <Show when={provider().status}>
              <span
                class="den-settings-provider-status"
                data-status={provider().status}
                data-configured={providerIsConfigured(provider())}
              >
                {provider().status}
              </span>
            </Show>
            <DenButton
              variant="link"
              class="den-settings-provider-remove"
              data-testid={`mcp-delete-provider-${provider().id}`}
              disabled={busy()}
              onClick={() => void deleteProvider(provider())}
            >
              {MCP_SETTINGS_COPY.delete}
            </DenButton>
          </header>

          <DenCheckbox
            class="den-settings-mcp-enable"
            checked={provider().enabled}
            disabled={busy() || projectCannotEnable(provider())}
            data-testid={`mcp-enable-${provider().id}`}
            onChange={(e) => void toggleEnabled(provider(), e.currentTarget.checked)}
          >
            {MCP_SETTINGS_COPY.columnEnabled}
          </DenCheckbox>

          <SettingsGovernedGroup
            label="Tool loading"
            active={provider().enabled}
          >
            <DenCheckbox
              class="den-settings-mcp-enable"
              checked={provider().tool_loading === "always"}
              disabled={busy()}
              data-testid={`mcp-always-load-${provider().id}`}
              onChange={(e) =>
                void toggleAlwaysLoad(provider(), e.currentTarget.checked)
              }
            >
              {MCP_SETTINGS_COPY.toolLoadingAlways}
            </DenCheckbox>
            <p class="den-settings-hint">{MCP_SETTINGS_COPY.toolLoadingHint}</p>
          </SettingsGovernedGroup>

          <Show when={provider().connection_source}>
            <p class="den-settings-hint">
              {MCP_SETTINGS_COPY.columnSource}: {provider().connection_source}
            </p>
          </Show>
          <Show
            when={
              !projectLocal() &&
              (provider().profiles?.length ? provider().profiles : null)
            }
          >
            {(profiles) => (
              <p class="den-settings-hint">
                Workflow profiles: {profiles().join(", ")}
              </p>
            )}
          </Show>
          <InlineNotice
            notice={noticeFromCopy(
              provider().notice,
              provider().last_error,
              noticeScope(),
            )}
            testId={`mcp-provider-notice-${provider().id}`}
          />
          <Show
            when={
              provider().token_present ||
              provider().headers_present ||
              provider().env_present ||
              provider().signed_in ||
              provider().status === "needs_auth"
            }
          >
            <p
              class="den-settings-hint"
              data-testid={`mcp-auth-flags-${provider().id}`}
            >
              <Show when={provider().token_present}>
                <span>{MCP_SETTINGS_COPY.tokenConfigured} · </span>
              </Show>
              <Show when={provider().headers_present}>
                <span>{MCP_SETTINGS_COPY.headersConfigured} · </span>
              </Show>
              <Show when={provider().env_present}>
                <span>{MCP_SETTINGS_COPY.envConfigured} · </span>
              </Show>
              <Show when={provider().signed_in}>
                <span>{MCP_SETTINGS_COPY.signedIn}</span>
              </Show>
              <Show when={!provider().signed_in && provider().status === "needs_auth"}>
                <span>{MCP_SETTINGS_COPY.needsAuth}</span>
              </Show>
            </p>
          </Show>

          <Show
            when={projectCanEditConnection(provider())}
            fallback={
              <p
                class="den-settings-hint"
                data-testid={`mcp-project-device-managed-${provider().id}`}
              >
                {MCP_SETTINGS_COPY.projectDeviceManaged}
              </p>
            }
          >
            <section class="den-settings-mcp-connection">
              <h3 class="den-settings-subhead">
                {MCP_SETTINGS_COPY.connectionHeading}
              </h3>
              <Show when={provider().docs_url} keyed>
                {(url) => (
                  <DenButton
                    variant="ghost"
                    compact
                    data-testid={`mcp-docs-${provider().id}`}
                    onClick={() => void confirmAndOpenExternalLink(url)}
                  >
                    {MCP_SETTINGS_COPY.recipeDocs}
                  </DenButton>
                )}
              </Show>
              <McpConnectionFields
                draft={connectionDraft}
                setDraft={setConnectionDraft}
                projectScope={projectLocal()}
                tokenLabel={provider().credential_label}
                tokenHint={provider().credential_hint}
                showToken={provider().auth !== "none"}
                lockCredentialWire={providerLocksCredentialWire(provider())}
              />
              <InlineNotice
                notice={connectionError()}
                testId="mcp-connection-error"
              />
              <DenButton
                variant="secondary"
                data-testid={`mcp-save-connection-${provider().id}`}
                disabled={busy()}
                onClick={() => void saveConnection(provider())}
              >
                {MCP_SETTINGS_COPY.saveConnection}
              </DenButton>
            </section>
          </Show>

          <div class="den-settings-mcp-row-actions">
            <Show when={providerOffersSignIn(provider(), projectLocal())}>
              <Show
                when={provider().signed_in}
                fallback={
                  <DenButton
                    variant="secondary"
                    data-testid={`mcp-oauth-signin-${provider().id}`}
                    disabled={busy() || oauthBusyId() === provider().id}
                    onClick={() => void startSignIn(provider())}
                  >
                    {MCP_SETTINGS_COPY.signIn}
                  </DenButton>
                }
              >
                <DenButton
                  variant="secondary"
                  data-testid={`mcp-oauth-signout-${provider().id}`}
                  disabled={busy() || oauthBusyId() === provider().id}
                  onClick={() => void signOut(provider())}
                >
                  {MCP_SETTINGS_COPY.signOut}
                </DenButton>
              </Show>
            </Show>
            <DenButton
              variant="secondary"
              data-testid={`mcp-tools-${provider().id}`}
              disabled={busy()}
              onClick={() => void toggleTools(provider())}
            >
              {MCP_SETTINGS_COPY.tools}
            </DenButton>
            <DenButton
              variant="secondary"
              data-testid={`mcp-resync-${provider().id}`}
              disabled={busy() || resyncingId() === provider().id}
              onClick={() => void resyncProvider(provider())}
            >
              {resyncingId() === provider().id
                ? MCP_SETTINGS_COPY.resyncing
                : MCP_SETTINGS_COPY.resync}
            </DenButton>
          </div>

          <Show when={toolsOpenId() === provider().id}>
            <div data-testid={`mcp-tools-panel-${provider().id}`}>
              <Show when={!provider().enabled}>
                <p class="den-settings-hint" data-testid={`mcp-tools-disabled-${provider().id}`}>
                  {MCP_SETTINGS_COPY.toolsDisabled}
                </p>
              </Show>
              <Show when={toolsLoadingId() === provider().id}>
                <p class="den-settings-hint">{MCP_SETTINGS_COPY.toolsLoading}</p>
              </Show>
              <Show
                when={
                  toolsLoadingId() !== provider().id &&
                  settledEmpty(
                    toolsById()[provider().id] ?? unloaded<McpToolInfo[]>(),
                    (tools) => tools.length === 0,
                  )
                }
              >
                <p class="den-settings-hint">{MCP_SETTINGS_COPY.toolsEmpty}</p>
              </Show>
              <Show
                when={
                  toolsLoadingId() !== provider().id &&
                  toolsById()[provider().id]?.state === "error"
                }
              >
                <p class="den-settings-hint" data-testid={`mcp-tools-unavailable-${provider().id}`}>
                  {MCP_SETTINGS_COPY.toolsUnavailable}
                </p>
              </Show>
              <ul class="den-settings-mcp-tools-list">
                <For each={valueOf(toolsById()[provider().id] ?? unloaded<McpToolInfo[]>()) ?? []}>
                  {(tool) => (
                    <li data-testid={`mcp-tool-${provider().id}-${tool.name}`}>
                      <code>{tool.name}</code>
                      <Show when={tool.description}>
                        <span class="den-settings-hint">
                          {" "}
                          — {tool.description}
                        </span>
                      </Show>
                    </li>
                  )}
                </For>
              </ul>
            </div>
          </Show>
        </article>
      </div>
    );
  };

  const renderAddDialog = () => (
    <Show when={addOpen()}>
      <ResidentPortal mount={document.body}>
        <div
          class="den-dialog-backdrop den-dialog-backdrop--viewport"
          data-testid="mcp-add-dialog"
          onClick={(e) => {
            if (e.target === e.currentTarget) closeAdd();
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={addDialogRef}
            class={
              addStep() === "custom"
                ? "den-dialog"
                : "den-dialog den-dialog--sheet"
            }
            role="dialog"
            aria-modal="true"
            aria-labelledby="mcp-editor-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="mcp-editor-title">
                {addStep() === "custom"
                  ? addCustomClass() === "web"
                    ? MCP_SETTINGS_COPY.addDialogCustomWebTitle
                    : MCP_SETTINGS_COPY.addDialogCustomLocalTitle
                  : MCP_SETTINGS_COPY.addDialogTitle}
              </h2>
            </header>
            <Scrollport
              class="den-settings-add-dialog__body"
              contentClass="den-settings-add-dialog__body-content"
              eager
            >
              <Show
                when={addStep() === "custom"}
                fallback={
                  <>
                    <p class="den-dialog__hint">{MCP_SETTINGS_COPY.addPickHint}</p>
                    <div data-testid="mcp-recipe-list">
                      <For each={recipeGroups()}>
                        {(group) => (
                          <SettingsListGroup
                            testId={`mcp-recipe-group-${group.kind}`}
                            label={group.label}
                          >
                            <ul class="den-settings-mcp-recipes">
                              <For each={group.recipes}>
                                {(recipe) => (
                                  <li>
                                    <button
                                      type="button"
                                      class="den-settings-mcp-recipe"
                                      data-testid={`mcp-recipe-${recipe.id}`}
                                      disabled={addBusy()}
                                      onClick={() => void addRecipe(recipe)}
                                    >
                                      <span class="den-settings-mcp-recipe__label">
                                        {recipe.label}
                                      </span>
                                      <Show when={recipe.added}>
                                        <span class="den-settings-hint">
                                          {MCP_SETTINGS_COPY.recipeAdded}
                                        </span>
                                      </Show>
                                      <span class="den-settings-hint">
                                        {recipe.hint}
                                      </span>
                                      <Show when={(recipe.env_keys ?? []).length > 0}>
                                        <span
                                          class="den-settings-hint"
                                          data-testid={`mcp-recipe-env-${recipe.id}`}
                                        >
                                          {MCP_SETTINGS_COPY.recipeEnvKeys(
                                            (recipe.env_keys ?? []).map(
                                              (key) => key.label,
                                            ),
                                          )}
                                        </span>
                                      </Show>
                                    </button>
                                    <Show when={recipe.docs_url} keyed>
                                      {(url) => (
                                        <button
                                          type="button"
                                          class="den-settings-mcp-recipe__docs"
                                          data-testid={`mcp-recipe-docs-${recipe.id}`}
                                          disabled={addBusy()}
                                          onClick={() =>
                                            void confirmAndOpenExternalLink(url)
                                          }
                                        >
                                          {MCP_SETTINGS_COPY.recipeDocs}
                                        </button>
                                      )}
                                    </Show>
                                  </li>
                                )}
                              </For>
                              <li>
                                <button
                                  type="button"
                                  class="den-settings-mcp-recipe"
                                  data-testid={
                                    group.kind === "web"
                                      ? "mcp-recipe-custom-web"
                                      : "mcp-recipe-custom-local"
                                  }
                                  disabled={addBusy()}
                                  onClick={() => {
                                    setAddError(undefined);
                                    setAddCustomClass(group.kind);
                                    setAddDraft({
                                      ...emptyAddDraft(),
                                      transport: "http",
                                    });
                                    setAddStep("custom");
                                  }}
                                >
                                  <span class="den-settings-mcp-recipe__label">
                                    {group.kind === "web"
                                      ? MCP_SETTINGS_COPY.addCustomWeb
                                      : MCP_SETTINGS_COPY.addCustomLocal}
                                  </span>
                                  <span class="den-settings-hint">
                                    {group.kind === "web"
                                      ? MCP_SETTINGS_COPY.addCustomWebHint
                                      : MCP_SETTINGS_COPY.addCustomLocalHint}
                                  </span>
                                </button>
                              </li>
                            </ul>
                          </SettingsListGroup>
                        )}
                      </For>
                    </div>
                    <InlineNotice notice={addError()} testId="mcp-editor-error" />
                  </>
                }
              >
                <p class="den-settings-hint">
                  {addCustomClass() === "web"
                    ? MCP_SETTINGS_COPY.addCustomWebHint
                    : MCP_SETTINGS_COPY.addCustomLocalHint}
                </p>
                <McpConnectionFields
                  draft={addDraft}
                  setDraft={(updater) =>
                    setAddDraft((d) => ({ ...d, ...updater(d) }))
                  }
                  includeId
                  projectScope={projectLocal()}
                  customClass={addCustomClass()}
                  idValue={() => addDraft().id}
                  onIdChange={(v) => setAddDraft((d) => ({ ...d, id: v }))}
                />
                <InlineNotice notice={addError()} testId="mcp-editor-error" />
              </Show>
            </Scrollport>
            <footer class="den-settings-add-dialog__footer">
              <Show
                when={addStep() === "custom"}
                fallback={
                  <DenButton
                    variant="secondary"
                    compact
                    disabled={addBusy()}
                    onClick={() => closeAdd()}
                  >
                    {MCP_SETTINGS_COPY.cancel}
                  </DenButton>
                }
              >
                <DenButton
                  variant="secondary"
                  compact
                  disabled={addBusy()}
                  onClick={() => {
                    setAddError(undefined);
                    setAddStep("pick");
                  }}
                >
                  {MCP_SETTINGS_COPY.pickBack}
                </DenButton>
                <DenButton
                  variant="primary"
                  compact
                  data-testid="mcp-editor-save"
                  disabled={addBusy() || !addReady()}
                  onClick={() => void saveAdd()}
                >
                  {MCP_SETTINGS_COPY.save}
                </DenButton>
              </Show>
            </footer>
          </div>
        </div>
      </ResidentPortal>
    </Show>
  );

  const renderOauthDialog = () => (
    <Show when={oauthPending()} keyed>
      {(pending) => (
        <ResidentPortal mount={document.body}>
          <div
            class="den-dialog-backdrop den-dialog-backdrop--viewport"
            data-testid="mcp-oauth-complete-dialog"
            onClick={(e) => {
              if (e.target === e.currentTarget && !oauthBusyId()) {
                void cancelSignIn();
              }
            }}
          >
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={oauthDialogRef}
              class="den-dialog den-dialog--sheet"
              role="dialog"
              aria-modal="true"
              aria-labelledby="mcp-oauth-title"
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id="mcp-oauth-title">{MCP_SETTINGS_COPY.oauthCodeTitle}</h2>
              </header>
              <Scrollport
                class="den-settings-add-dialog__body"
                contentClass="den-settings-add-dialog__body-content"
                eager
              >
                <Show
                  when={oauthManual()}
                  fallback={
                    <>
                      <p class="den-dialog__hint" data-testid="mcp-oauth-waiting">
                        {MCP_SETTINGS_COPY.oauthWaitingHint}
                      </p>
                      <DenButton
                        variant="ghost"
                        compact
                        data-testid="mcp-oauth-manual"
                        onClick={() => setOauthManual(true)}
                      >
                        {MCP_SETTINGS_COPY.oauthManualToggle}
                      </DenButton>
                    </>
                  }
                >
                  <p class="den-dialog__hint">{MCP_SETTINGS_COPY.oauthCodeHint}</p>
                  <DenField label={MCP_SETTINGS_COPY.fieldOAuthCode}>
                    <DenInput
                      id="mcp-oauth-code"
                      data-testid="mcp-oauth-code"
                      autocomplete="off"
                      value={oauthCode()}
                      onInput={(e) => setOauthCode(e.currentTarget.value)}
                    />
                  </DenField>
                </Show>
                <InlineNotice notice={oauthError()} testId="mcp-oauth-error" />
              </Scrollport>
              <footer class="den-settings-add-dialog__footer">
                <DenButton
                  variant="secondary"
                  compact
                  disabled={Boolean(oauthBusyId())}
                  onClick={() => void cancelSignIn()}
                >
                  {MCP_SETTINGS_COPY.cancel}
                </DenButton>
                <Show when={oauthManual()}>
                  <DenButton
                    variant="primary"
                    compact
                    data-testid={`mcp-oauth-complete-${pending.id}`}
                    disabled={Boolean(oauthBusyId()) || !oauthCode().trim()}
                    onClick={() => void completeSignIn()}
                  >
                    {MCP_SETTINGS_COPY.oauthComplete}
                  </DenButton>
                </Show>
              </footer>
            </div>
          </div>
        </ResidentPortal>
      )}
    </Show>
  );

  return (
    <div
      class="den-settings-editor"
      classList={{ "den-settings-editor--project": projectLocal() }}
      data-testid="mcp-settings-panel"
      data-scope={projectLocal() ? "project" : "global"}
    >
      <SettingsEditorTitle
        icon={<McpIcon />}
        scopeBadge={
          projectLocal() ? PROJECT_SETTINGS_OVERLAY_COPY.badge : undefined
        }
      >
        {MCP_SETTINGS_COPY.title}
      </SettingsEditorTitle>

      <SettingsScopeLede
        scope={projectLocal() ? "project" : "device"}
        counterpartLabel={props.counterpartLabel}
        onOpenCounterpart={props.onOpenCounterpart}
      >
        <p class="den-settings-hint" data-testid="mcp-access-explainer">
          {MCP_SETTINGS_COPY.accessExplainer}
        </p>
      </SettingsScopeLede>

      <div class="den-settings-prefs-band">
        <div
          class="den-settings-hint den-settings-prefs-band__warn"
          data-testid="mcp-threat-banner"
          role="note"
        >
          <p data-testid="mcp-threat-local">{MCP_SETTINGS_COPY.threatLocal}</p>
          <Show when={!projectLocal()}>
            <p data-testid="mcp-threat-web">{MCP_SETTINGS_COPY.threatWeb}</p>
          </Show>
          <p>
            See {MCP_SETTINGS_COPY.threatDocs}.
          </p>
        </div>
        <Show when={projectLocal() && providerQuery.ready()}>
          <div class="den-settings-prefs-band__warn">
            <ProjectSettingsOverrideControl
              settingsPath={PROJECT_SETTINGS_OVERLAY_COPY.settingsPath.mcp}
              enabled={projectOverrideEnabled()}
              disabled={busyOverride() || loading()}
              followingSummary={followingSummary()}
              onChange={(enabled) => void applyProjectOverride(enabled)}
            />
          </div>
        </Show>
        <div
          class="den-settings-hint den-settings-prefs-band__warn"
          data-testid="mcp-path-hints"
        >
          <strong>{MCP_SETTINGS_COPY.pathHintsTitle}</strong>
          {" · "}
          {MCP_SETTINGS_COPY.pathHintUser}
          {" · "}
          {MCP_SETTINGS_COPY.pathHintProject}
        </div>
      </div>

      <InlineNotice notice={loadError()} testId="mcp-load-error" />

      <Show when={listVisible() && providerQuery.ready()}>
        <SettingsListPanel
          testId="mcp-list-panel"
          setting={projectLocal() ? undefined : "mcp-providers"}
          class="den-settings-mcp-inbox"
          chrome={
            selectedProvider() != null ? (
              <SettingsListBackChrome
                testId="mcp-back"
                label={MCP_SETTINGS_COPY.providersBack}
                onBack={closeDetail}
              />
            ) : (
              <SettingsListChrome
                testId="mcp-settings-chrome"
                count={
                  <Show when={providerQuery.value() !== undefined}>
                    <span data-testid="mcp-provider-count">
                      {providerCountLabel(listProviders().length)}
                    </span>
                  </Show>
                }
                secondaryAction={
                  !projectLocal() ? (
                    <DenButton
                      variant="ghost"
                      compact
                      data-testid="mcp-test-connection"
                      disabled={checkRunning() || loading()}
                      onClick={() => void testConnection()}
                    >
                      {checkRunning()
                        ? MCP_SETTINGS_COPY.testingConnection
                        : MCP_SETTINGS_COPY.testConnection}
                    </DenButton>
                  ) : undefined
                }
                action={
                  <DenButton
                    variant="primary"
                    compact
                    data-testid="mcp-add-provider"
                    {...settingControl()}
                    disabled={loading() || addBusy()}
                    onClick={openAdd}
                  >
                    {settingLabel("mcp-providers")}
                  </DenButton>
                }
              />
            )
          }
        >
          <Show when={!projectLocal() && selectedProvider() == null}>
            <InlineNotice notice={checkError()} testId="mcp-check-error" />
            <Show when={checkRows()}>
              {(rows) => (
                <div
                  class="den-settings-mcp-connection--compact"
                  data-testid="mcp-connection-results"
                >
                  <header class="den-settings-mcp-connection-head">
                    <h3 class="den-settings-subhead">
                      {MCP_SETTINGS_COPY.connectionResultsTitle}
                    </h3>
                    <DenButton
                      variant="ghost"
                      compact
                      data-testid="mcp-connection-dismiss"
                      onClick={() => setCheckRows(undefined)}
                    >
                      {MCP_SETTINGS_COPY.connectionResultsDismiss}
                    </DenButton>
                  </header>
                  <ul class="den-settings-mcp-connection-list">
                    <For each={rows()}>
                      {(row) => (
                        <li
                          class="den-settings-mcp-connection-row"
                          data-testid={`mcp-connection-row-${row.provider_id ?? ""}`}
                        >
                          <span class="den-settings-mcp-connection-row__id">
                            {row.provider_id ?? MCP_SETTINGS_COPY.unnamedOverlayRow}
                          </span>
                          <span
                            class="den-settings-provider-status"
                            data-status={row.status === "healthy" ? "ok" : row.status}
                            data-configured={row.status === "healthy"}
                          >
                            {row.status === "healthy"
                              ? MCP_SETTINGS_COPY.statusOk
                              : row.status === "disabled"
                                ? MCP_SETTINGS_COPY.statusDisabled
                                : MCP_SETTINGS_COPY.statusError}
                          </span>
                          <Show
                            when={noticeFromCopy(
                              row.notice,
                              row.code,
                              noticeScope(),
                            )}
                            keyed
                            fallback={
                              <span class="den-settings-hint">—</span>
                            }
                          >
                            {(notice) => (
                              <InlineNotice
                                notice={notice}
                                testId={`mcp-connection-notice-${row.provider_id}`}
                              />
                            )}
                          </Show>
                        </li>
                      )}
                    </For>
                  </ul>
                </div>
              )}
            </Show>
          </Show>

          <Show when={providerQuery.showLoading()}>
            <p class="den-settings-hint">Loading…</p>
          </Show>

          <ListSurfaceInbox
            detailOpen={selectedProvider() != null}
            isEmpty={providerQuery.value() !== undefined && listProviders().length === 0}
            listTestId="mcp-provider-list"
            detailTestId="mcp-provider-detail"
            empty={
              <p
                class="den-settings-hint den-settings-list-inbox__empty"
                data-testid="mcp-empty-providers"
              >
                {MCP_SETTINGS_COPY.emptyProviders}
              </p>
            }
            list={
              <For each={listGroups()}>
                {(group) => (
                  <SettingsListGroup
                    testId={`mcp-provider-group-${group.kind}`}
                    label={group.label}
                  >
                    <For each={group.providers}>
                      {(provider) => renderListRow(provider)}
                    </For>
                  </SettingsListGroup>
                )}
              </For>
            }
            detail={
              <ShowLatest when={selectedProvider()} by={(provider) => provider.id}>
                {(provider) => (
                  <Show
                    when={isRejectedProvider(provider())}
                    fallback={renderProviderDetail(provider)}
                  >
                    {renderRejectedDetail(provider)}
                  </Show>
                )}
              </ShowLatest>
            }
          />
        </SettingsListPanel>
      </Show>

      <Show when={!listVisible() && providerQuery.showLoading()}>
        <p class="den-settings-hint">Loading…</p>
      </Show>

      {renderAddDialog()}
      {renderOauthDialog()}
    </div>
  );
}
