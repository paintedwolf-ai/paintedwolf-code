import { For, Show, type Accessor, type Setter } from "solid-js";
import type { McpProvider, McpToolInfo } from "../../../api/types.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { noticeFromCopy, type AppNotice } from "../../../notices/notice-model.ts";
import type { NoticeScope } from "../../../notices/notice-scope.ts";
import { confirmAndOpenExternalLink } from "../../../platform/desktop/external-link.ts";
import {
  isRejectedProvider,
  providerLocksCredentialWire,
  providerOffersSignIn,
} from "../../../settings/mcp/mcp-provider-model.ts";
import { MCP_SETTINGS_COPY } from "../../../settings/mcp/mcp-settings-copy.ts";
import { settledEmpty, unloaded, valueOf, type LoadState } from "../../../store/load-state.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { SettingsGovernedGroup } from "../SettingsGovernedGroup.tsx";
import { SettingsListRow } from "../SettingsListRow.tsx";
import type { ConnectionDraft } from "./mcp-connection-draft.ts";
import { McpConnectionFields } from "./McpConnectionFields.tsx";
import { projectDeviceManaged } from "./mcp-provider-actions.ts";

export function providerIsConfigured(provider: McpProvider): boolean {
  if (isRejectedProvider(provider)) return false;
  if (provider.status === "ready") return true;
  if (provider.status === "needs_auth" || provider.status === "error") return false;
  return Boolean(provider.enabled) && !provider.last_error;
}

export function McpProviderListRow(props: {
  provider: McpProvider;
  onSelect: (provider: McpProvider) => void;
}) {
  const rejected = () => isRejectedProvider(props.provider);

  return (
    <SettingsListRow
      testId={rejected() ? "mcp-rejected-row" : `mcp-provider-${props.provider.id}`}
      variant={rejected() ? "rejected" : "default"}
      primary={props.provider.id}
      secondary={
        !rejected() && props.provider.transport ? (
          <span data-testid={`mcp-transport-${props.provider.id}`}>
            {props.provider.transport}
          </span>
        ) : undefined
      }
      status={
        rejected() || props.provider.status ? (
          <span
            class="den-settings-provider-status"
            data-testid={`mcp-status-${props.provider.id}`}
            data-status={props.provider.status}
            data-configured={providerIsConfigured(props.provider)}
          >
            {rejected() ? MCP_SETTINGS_COPY.rejectedLabel : props.provider.status}
          </span>
        ) : undefined
      }
      onSelect={() => props.onSelect(props.provider)}
    />
  );
}

export function McpRejectedProviderDetail(props: {
  provider: Accessor<McpProvider>;
  noticeScope: NoticeScope;
}) {
  return (
    <div
      class="den-settings-mcp-detail-wrap"
      data-testid="mcp-rejected-detail"
    >
      <article class="den-settings-provider-card">
        <header class="den-settings-provider-head">
          <strong>{props.provider().id}</strong>
          <span
            class="den-settings-provider-status"
            data-configured={false}
          >
            {MCP_SETTINGS_COPY.rejectedLabel}
          </span>
        </header>
        <InlineNotice
          notice={noticeFromCopy(
            props.provider().notice,
            props.provider().last_error,
            props.noticeScope,
          )}
          testId="mcp-rejected-notice"
        />
        <Show when={props.provider().connection_source}>
          <p class="den-settings-hint">
            {MCP_SETTINGS_COPY.columnSource}: {props.provider().connection_source}
          </p>
        </Show>
      </article>
    </div>
  );
}

export type McpProviderDetailCardProps = {
  provider: Accessor<McpProvider>;
  projectLocal: boolean;
  noticeScope: NoticeScope;
  togglingId: () => string | undefined;
  busyOverride: () => boolean;
  connectionBusy: () => boolean;
  addBusy: () => boolean;
  connectionDraft: Accessor<ConnectionDraft>;
  setConnectionDraft: Setter<ConnectionDraft>;
  connectionError: () => AppNotice | undefined;
  oauthBusyId: () => string | undefined;
  toolsOpenId: () => string | undefined;
  toolsLoadingId: () => string | undefined;
  toolsById: () => Record<string, LoadState<McpToolInfo[]>>;
  resyncingId: () => string | undefined;
  onDeleteProvider: (provider: McpProvider) => void;
  onToggleEnabled: (provider: McpProvider, enabled: boolean) => void;
  onToggleAlwaysLoad: (provider: McpProvider, always: boolean) => void;
  onSaveConnection: (provider: McpProvider) => void;
  onStartSignIn: (provider: McpProvider) => void;
  onSignOut: (provider: McpProvider) => void;
  onToggleTools: (provider: McpProvider) => void;
  onResyncProvider: (provider: McpProvider) => void;
};

export function McpProviderDetailCard(props: McpProviderDetailCardProps) {
  const provider = () => props.provider();
  const busy = () =>
    props.togglingId() === provider().id ||
    props.busyOverride() ||
    props.connectionBusy() ||
    props.addBusy();

  const projectCannotEnable = (item: McpProvider) =>
    props.projectLocal && !item.enabled && projectDeviceManaged(item);

  const projectCanEditConnection = (item: McpProvider) =>
    !props.projectLocal ||
    (item.transport === "http" && !projectDeviceManaged(item));

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
            onClick={() => props.onDeleteProvider(provider())}
          >
            {MCP_SETTINGS_COPY.delete}
          </DenButton>
        </header>

        <DenCheckbox
          class="den-settings-mcp-enable"
          checked={provider().enabled}
          disabled={busy() || projectCannotEnable(provider())}
          data-testid={`mcp-enable-${provider().id}`}
          onChange={(e) => props.onToggleEnabled(provider(), e.currentTarget.checked)}
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
              props.onToggleAlwaysLoad(provider(), e.currentTarget.checked)
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
            !props.projectLocal &&
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
            props.noticeScope,
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
              draft={props.connectionDraft}
              setDraft={props.setConnectionDraft}
              projectScope={props.projectLocal}
              tokenLabel={provider().credential_label}
              tokenHint={provider().credential_hint}
              showToken={provider().auth !== "none"}
              lockCredentialWire={providerLocksCredentialWire(provider())}
            />
            <InlineNotice
              notice={props.connectionError()}
              testId="mcp-connection-error"
            />
            <DenButton
              variant="secondary"
              data-testid={`mcp-save-connection-${provider().id}`}
              disabled={busy()}
              onClick={() => props.onSaveConnection(provider())}
            >
              {MCP_SETTINGS_COPY.saveConnection}
            </DenButton>
          </section>
        </Show>

        <div class="den-settings-mcp-row-actions">
          <Show when={providerOffersSignIn(provider(), props.projectLocal)}>
            <Show
              when={provider().signed_in}
              fallback={
                <DenButton
                  variant="secondary"
                  data-testid={`mcp-oauth-signin-${provider().id}`}
                  disabled={busy() || props.oauthBusyId() === provider().id}
                  onClick={() => props.onStartSignIn(provider())}
                >
                  {MCP_SETTINGS_COPY.signIn}
                </DenButton>
              }
            >
              <DenButton
                variant="secondary"
                data-testid={`mcp-oauth-signout-${provider().id}`}
                disabled={busy() || props.oauthBusyId() === provider().id}
                onClick={() => props.onSignOut(provider())}
              >
                {MCP_SETTINGS_COPY.signOut}
              </DenButton>
            </Show>
          </Show>
          <DenButton
            variant="secondary"
            data-testid={`mcp-tools-${provider().id}`}
            disabled={busy()}
            onClick={() => props.onToggleTools(provider())}
          >
            {MCP_SETTINGS_COPY.tools}
          </DenButton>
          <DenButton
            variant="secondary"
            data-testid={`mcp-resync-${provider().id}`}
            disabled={busy() || props.resyncingId() === provider().id}
            onClick={() => props.onResyncProvider(provider())}
          >
            {props.resyncingId() === provider().id
              ? MCP_SETTINGS_COPY.resyncing
              : MCP_SETTINGS_COPY.resync}
          </DenButton>
        </div>

        <Show when={props.toolsOpenId() === provider().id}>
          <div data-testid={`mcp-tools-panel-${provider().id}`}>
            <Show when={!provider().enabled}>
              <p class="den-settings-hint" data-testid={`mcp-tools-disabled-${provider().id}`}>
                {MCP_SETTINGS_COPY.toolsDisabled}
              </p>
            </Show>
            <Show when={props.toolsLoadingId() === provider().id}>
              <p class="den-settings-hint">{MCP_SETTINGS_COPY.toolsLoading}</p>
            </Show>
            <Show
              when={
                props.toolsLoadingId() !== provider().id &&
                settledEmpty(
                  props.toolsById()[provider().id] ?? unloaded<McpToolInfo[]>(),
                  (tools) => tools.length === 0,
                )
              }
            >
              <p class="den-settings-hint">{MCP_SETTINGS_COPY.toolsEmpty}</p>
            </Show>
            <Show
              when={
                props.toolsLoadingId() !== provider().id &&
                props.toolsById()[provider().id]?.state === "error"
              }
            >
              <p class="den-settings-hint" data-testid={`mcp-tools-unavailable-${provider().id}`}>
                {MCP_SETTINGS_COPY.toolsUnavailable}
              </p>
            </Show>
            <ul class="den-settings-mcp-tools-list">
              <For each={valueOf(props.toolsById()[provider().id] ?? unloaded<McpToolInfo[]>()) ?? []}>
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
}
