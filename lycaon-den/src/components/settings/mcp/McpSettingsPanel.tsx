import { createMcpProviderTools } from "./mcp-provider-tools.ts";
import { createMcpConnectionEditor } from "./mcp-connection-editor.ts";
import { createMcpAddProvider } from "./mcp-add-provider.ts";
import { createMcpProviderActions } from "./mcp-provider-actions.ts";
import { createMcpSignIn } from "./mcp-sign-in.ts";
import { settingControl, settingLabel } from "../../../settings/settings-registry.ts";
import { useResidentLive } from "../../../ui/resident-activity.ts";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { For, Show, createEffect, createMemo, createSignal, on, type Accessor } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { McpProvider } from "../../../api/types.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { noticeFromCaught, noticeFromCopy, type AppNotice } from "../../../notices/notice-model.ts";
import { APP_SCOPE, projectScope } from "../../../notices/notice-scope.ts";
import {
  catalogProviders,
  groupProvidersByClass,
  groupRecipesByClass,
  isRejectedProvider,
  providersForScope,
} from "../../../settings/mcp/mcp-provider-model.ts";
import { MCP_SETTINGS_COPY } from "../../../settings/mcp/mcp-settings-copy.ts";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../../settings/security/project-settings-overlay-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { SettingsEditorTitle, McpIcon } from "../SettingsEditorTitle.tsx";
import { ProjectSettingsOverrideControl } from "../ProjectSettingsOverrideControl.tsx";
import { SettingsScopeLede } from "../SettingsScopeLede.tsx";
import { SettingsListBackChrome } from "../SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../SettingsListChrome.tsx";
import { SettingsListGroup } from "../SettingsListGroup.tsx";
import { ListSurfaceInbox } from "../../list/ListSurfaceInbox.tsx";
import { SettingsListPanel } from "../SettingsListPanel.tsx";
import { draftFromProvider } from "./mcp-connection-draft.ts";
import {
  McpProviderDetailCard,
  McpProviderListRow,
  McpRejectedProviderDetail,
} from "./McpProviderDetail.tsx";
import { McpProviderDialogs } from "./McpProviderDialogs.tsx";

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

  const projectLocal = () => props.alwaysProjectScope === true;
  const projectId = () =>
    projectLocal() ? props.projectId : undefined;
  const noticeScope = () =>
    projectLocal() && props.projectId
      ? projectScope(props.projectId)
      : APP_SCOPE;
  const catchNotice = (err: unknown) => noticeFromCaught(err, noticeScope());
  observeOverride();

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
                      {(provider) => (
                        <McpProviderListRow
                          provider={provider}
                          onSelect={selectProvider}
                        />
                      )}
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
                    fallback={
                      <McpProviderDetailCard
                        provider={provider}
                        projectLocal={projectLocal()}
                        noticeScope={noticeScope()}
                        togglingId={togglingId}
                        busyOverride={busyOverride}
                        connectionBusy={connectionBusy}
                        addBusy={addBusy}
                        connectionDraft={connectionDraft}
                        setConnectionDraft={setConnectionDraft}
                        connectionError={connectionError}
                        oauthBusyId={oauthBusyId}
                        toolsOpenId={toolsOpenId}
                        toolsLoadingId={toolsLoadingId}
                        toolsById={toolsById}
                        resyncingId={resyncingId}
                        onDeleteProvider={(item) => void deleteProvider(item)}
                        onToggleEnabled={(item, enabled) => void toggleEnabled(item, enabled)}
                        onToggleAlwaysLoad={(item, always) => void toggleAlwaysLoad(item, always)}
                        onSaveConnection={(item) => void saveConnection(item)}
                        onStartSignIn={(item) => void startSignIn(item)}
                        onSignOut={(item) => void signOut(item)}
                        onToggleTools={(item) => void toggleTools(item)}
                        onResyncProvider={(item) => void resyncProvider(item)}
                      />
                    }
                  >
                    <McpRejectedProviderDetail
                      provider={provider}
                      noticeScope={noticeScope()}
                    />
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

      <McpProviderDialogs
        projectLocal={projectLocal}
        addOpen={addOpen}
        addStep={addStep}
        setAddStep={setAddStep}
        addCustomClass={addCustomClass}
        setAddCustomClass={setAddCustomClass}
        recipeGroups={recipeGroups}
        addDraft={addDraft}
        setAddDraft={setAddDraft}
        addError={addError}
        setAddError={setAddError}
        addBusy={addBusy}
        addReady={addReady}
        closeAdd={closeAdd}
        saveAdd={saveAdd}
        addRecipe={addRecipe}
        oauthPending={oauthPending}
        oauthBusyId={oauthBusyId}
        oauthManual={oauthManual}
        setOauthManual={setOauthManual}
        oauthCode={oauthCode}
        setOauthCode={setOauthCode}
        oauthError={oauthError}
        cancelSignIn={cancelSignIn}
        completeSignIn={completeSignIn}
      />
    </div>
  );
}
