import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import {
  For,
  Show,
  createMemo,
  createSignal,
  onCleanup,
} from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  HostResource,
  HostResourceAccess,
  HostResourceAccessSetting,
  HostResourceConnectionMode,
  HostResourceLocalServiceTransport,
  HostResourceStatus,
} from "../../../api/types.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { noticeFromCaught, type AppNotice } from "../../../notices/notice-model.ts";
import { APP_SCOPE } from "../../../notices/notice-scope.ts";
import { confirmAndOpenExternalLink } from "../../../platform/desktop/external-link.ts";
import { ListSurfaceInbox } from "../../list/ListSurfaceInbox.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import {
  HostResourcesIcon,
  SettingsEditorTitle,
} from "../SettingsEditorTitle.tsx";
import { SettingsListBackChrome } from "../SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../SettingsListChrome.tsx";
import { SettingsListGroup } from "../SettingsListGroup.tsx";
import { SettingsListPanel } from "../SettingsListPanel.tsx";
import { SettingsListRow } from "../SettingsListRow.tsx";
import { SettingsScopeLede } from "../SettingsScopeLede.tsx";
import { overlayRel } from "../../../platform/files/overlay-dir.ts";

type Props = { client: LycaonClient; embedded?: boolean };

const STATUS_LABEL: Record<HostResourceStatus, string> = {
  available: "Available",
  unavailable: "Unavailable",
  unknown: "Unknown",
};

const CONNECTION_LABEL: Record<HostResourceConnectionMode, string> = {
  none: "No exceptional connection",
  proxy: "HTTP proxy",
  socks: "SOCKS proxy",
  baseline_loopback: "Loopback",
  local_service: "Local service",
  direct_ip: "Direct IP",
  dynamic: "Resolved when used",
};

const TRANSPORT_LABEL: Record<HostResourceLocalServiceTransport, string> = {
  unix_socket: "Unix socket",
  named_pipe: "Windows named pipe",
};

const ACCESS_LABEL: Record<HostResourceAccess, string> = {
  allow: "Use normally",
  ask: "Ask before use",
  deny: "Blocked",
};

const PROMPT_LABEL = {
  omit: "Write agents when present",
  advertise: "Any agent that can use it",
  avoid: "Shown, prefer another route",
} as const;

function statusSummary(resource: HostResource): string {
  if (resource.host_support === "not_applicable") return "Not applicable on this platform";
  if (resource.host_support === "unsupported") return "Host boundary does not support this route";
  if (resource.status === "available" && resource.connections.length > 0) {
    return resource.connections
      .map((item) => item.transport ? TRANSPORT_LABEL[item.transport] : CONNECTION_LABEL[item.mode])
      .join(" · ");
  }
  return STATUS_LABEL[resource.status];
}

function reasonLabel(reason?: string): string {
  if (!reason) return "Detected";
  return reason.replaceAll("_", " ");
}

export function HostResourcesSettingsPanel(props: Props) {
  const query = createSurfaceQuery({
    name: "host-resources",
    source: () => ({ client: props.client, key: "device" }),
    load: ({ client }) => client.getHostResources(),
  });
  const snapshot = query.value;
  const [selectedId, setSelectedId] = createSignal<string>();
  const loading = query.loading;
  const [refreshing, setRefreshing] = createSignal(false);
  const [savingId, setSavingId] = createSignal<string>();
  const [actionError, setError] = createSignal<AppNotice | undefined>();
  const error = () => actionError() ?? (query.error() ? catchNotice(query.cause(), "Host resources could not be loaded.") : undefined);
  const catchNotice = (err: unknown, fallback: string) =>
    noticeFromCaught(err, APP_SCOPE, { title: fallback, message: fallback });
  let active = true;

  onCleanup(() => {
    active = false;
  });

  const refresh = async () => {
    const target = query.capture();
    setRefreshing(true);
    setError(undefined);
    try {
      const next = await props.client.refreshHostResources();
      if (active) target.publish(next);
    } catch (err) {
      if (active) setError(catchNotice(err, "Host resources could not be refreshed."));
    } finally {
      if (active) setRefreshing(false);
    }
  };

  const resources = () => snapshot()?.resources ?? [];

  const saveAccess = async (resource: HostResource, access: HostResourceAccessSetting) => {
    const target = query.capture();
    setSavingId(resource.id);
    setError(undefined);
    try {
      const next = await props.client.updateHostResource(resource.id, { access });
      if (active) target.publish(next);
    } catch (err) {
      if (active) setError(catchNotice(err, "Host-resource policy could not be saved."));
    } finally {
      if (active) setSavingId(undefined);
    }
  };
  const selected = createMemo(() =>
    resources().find((resource) => resource.id === selectedId()),
  );
  const groups = createMemo(() => {
    const grouped = new Map<string, HostResource[]>();
    for (const resource of resources()) {
      const entries = grouped.get(resource.category) ?? [];
      entries.push(resource);
      grouped.set(resource.category, entries);
    }
    return [...grouped.entries()]
      .map(([category, entries]) => ({
        category,
        entries: entries.sort((a, b) => a.label.localeCompare(b.label)),
      }))
      .sort((a, b) => a.category.localeCompare(b.category));
  });
  const availableCount = createMemo(
    () => resources().filter(
      (item) => item.status === "available" && item.host_support === "supported",
    ).length,
  );

  return (
    <div
      class="den-settings-editor"
      classList={{ "den-settings-editor--embedded": props.embedded === true }}
      data-testid="host-resources-settings-panel"
    >
      <div class="den-settings-header">
        <Show when={!props.embedded}>
          <SettingsEditorTitle icon={<HostResourcesIcon />}>
            Host resources
          </SettingsEditorTitle>
        </Show>
        <SettingsScopeLede scope="device">
          Host resources describe tools and services that may be present. Access policy can add one unified approval ask or block use; it never bypasses the sandbox or grants a connection by itself.
        </SettingsScopeLede>
      </div>

      <InlineNotice notice={error()} testId="host-resources-error" />
      <For each={snapshot()?.diagnostics ?? []}>
        {(diagnostic) => (
          <div class="den-resource-diagnostic" role="alert">
            <strong>{diagnostic.code}</strong>
            <span>{diagnostic.message}</span>
          </div>
        )}
      </For>

      <Show when={snapshot()?.user_catalog_path}>
        {(path) => (
          <p class="den-settings-hint den-resource-catalog-path">
            Add device host resources in <code>{path()}</code>.
          </p>
        )}
      </Show>

      <SettingsListPanel
        testId="host-resources-list-panel"
        chrome={
          selected() ? (
            <SettingsListBackChrome
              label="All host resources"
              testId="host-resources-back"
              onBack={() => setSelectedId(undefined)}
            />
          ) : (
            <SettingsListChrome
              testId="host-resources-list-chrome"
              count={
                <span data-testid="host-resources-count">
                  {loading() ? "Checking host resources…" : `${availableCount()} of ${resources().length} available`}
                </span>
              }
              action={
                <DenButton
                  variant="secondary"
                  compact
                  disabled={loading() || refreshing()}
                  data-testid="host-resources-refresh"
                  onClick={() => void refresh()}
                >
                  {refreshing() ? "Checking…" : "Refresh"}
                </DenButton>
              }
            />
          )
        }
      >
        <ListSurfaceInbox
          detailOpen={selected() != null}
          isEmpty={!loading() && resources().length === 0}
          listTestId="host-resources-list"
          detailTestId="host-resources-detail"
          empty={<p class="den-settings-hint den-settings-list-inbox__empty">No host resources are configured.</p>}
          list={
            <For each={groups()}>
              {(group) => (
                <SettingsListGroup
                  label={group.category}
                  meta={`${group.entries.filter(
                    (item) => item.status === "available" && item.host_support === "supported",
                  ).length} of ${group.entries.length}`}
                >
                  <For each={group.entries}>
                    {(resource) => (
                      <SettingsListRow
                        testId={`resource-row-${resource.id}`}
                        selected={resource.id === selectedId()}
                        variant={
                          resource.status === "unknown" || resource.host_support === "unsupported"
                            ? "warning"
                            : "default"
                        }
                        primary={resource.label}
                        secondary={resource.description}
                        status={
                          <span class="den-resource-status" data-status={resource.status}>
                            {statusSummary(resource)} · {ACCESS_LABEL[resource.access]}
                          </span>
                        }
                        onSelect={() => setSelectedId(resource.id)}
                      />
                    )}
                  </For>
                </SettingsListGroup>
              )}
            </For>
          }
          detail={
            <Show when={selected()} keyed>
              {(resource) => (
                <article class="den-settings-detail">
                  <header class="den-settings-detail__header">
                    <div>
                      <h3>{resource.label}</h3>
                      <p>{resource.description}</p>
                    </div>
                    <span class="den-resource-status" data-status={resource.status}>
                      {statusSummary(resource)}
                    </span>
                  </header>

                  <dl class="den-settings-facts">
                    <div><dt>Host resource ID</dt><dd><code>{resource.id}</code></dd></div>
                    <div><dt>Family</dt><dd><code>{resource.family}</code></dd></div>
                    <div><dt>Source</dt><dd>{resource.origin === "built-in" ? "Built in" : "User catalog"}</dd></div>
                    <div><dt>Discovery</dt><dd>{reasonLabel(resource.reason)}</dd></div>
                    <div><dt>Host support</dt><dd>{resource.host_support.replaceAll("_", " ")}</dd></div>
                    <div><dt>Agent surfaces</dt><dd>{resource.surfaces.join(", ")}</dd></div>
                    <div><dt>Access</dt><dd>{ACCESS_LABEL[resource.access]}</dd></div>
                    <div><dt>Model guidance</dt><dd>{PROMPT_LABEL[resource.prompt]}</dd></div>
                    <div><dt>Connection</dt><dd>{statusSummary(resource)}</dd></div>
                    <div><dt>Last checked</dt><dd>{new Date(resource.checked_at).toLocaleString()}</dd></div>
                  </dl>

                  <section class="den-resource-connections">
                    <h4>Access policy</h4>
                    <p class="den-settings-hint">
                      Ask uses the normal Approvals card and active grants, so concurrent identical actions coalesce and approved scopes do not re-alert. Block is enforced before connection approval.
                    </p>
                    <DenSelect
                      aria-label={`Access policy for ${resource.label}`}
                      data-testid={`resource-access-${resource.id}`}
                      disabled={savingId() === resource.id}
                      value={resource.access_setting}
                      options={[
                        { value: "inherit", label: `Inherit (${ACCESS_LABEL[resource.access]})` },
                        { value: "ask", label: "Ask before use" },
                        { value: "deny", label: "Block" },
                      ]}
                      onValueChange={(value) =>
                        void saveAccess(resource, value as HostResourceAccessSetting)
                      }
                    />
                    <p class="den-settings-hint">
                      Ambient model guidance is configured in <code>{snapshot()?.user_catalog_path}</code>; projects with approved settings may tighten guidance in <code>{overlayRel("host-resources.yaml")}</code>.
                    </p>
                  </section>

                  <Show when={resource.connections.length > 0}>
                    <section class="den-resource-connections">
                      <h4>Host-validated routes</h4>
                      <For each={resource.connections}>
                        {(connection) => (
                          <div>
                            <span>
                              {connection.transport
                                ? TRANSPORT_LABEL[connection.transport]
                                : CONNECTION_LABEL[connection.mode]}
                            </span>
                            <Show when={connection.target} keyed>
                              {(target) => <code>{target}</code>}
                            </Show>
                          </div>
                        )}
                      </For>
                    </section>
                  </Show>

                  <Show when={resource.docs_url} keyed>
                    {(url) => (
                      <DenButton
                        variant="link"
                        class="den-resource-docs"
                        onClick={() => void confirmAndOpenExternalLink(url)}
                      >
                        Open documentation
                      </DenButton>
                    )}
                  </Show>
                </article>
              )}
            </Show>
          }
        />
      </SettingsListPanel>
    </div>
  );
}
