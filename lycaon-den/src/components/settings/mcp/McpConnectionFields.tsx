import { Show, type Accessor } from "solid-js";
import type { McpCredentialWire } from "../../../api/types.ts";
import { MCP_SETTINGS_COPY, mcpHttpUrlFieldMessage } from "../../../settings/mcp/mcp-settings-copy.ts";
import { classifyMcpHttpUrl } from "../../../settings/mcp/mcp-http-url.ts";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { type ConnectionDraft } from "./mcp-connection-draft.ts";

type ConnectionFieldsProps = {
  draft: Accessor<ConnectionDraft>;
  setDraft: (updater: (d: ConnectionDraft) => ConnectionDraft) => void;
  includeId?: boolean;
  projectScope?: boolean;
  /** Custom add path locks the form to one host class. */
  customClass?: "local" | "web";
  /** Accessor so the add dialog does not remount on every keystroke. */
  idValue?: Accessor<string>;
  onIdChange?: (value: string) => void;
  tokenLabel?: string;
  tokenHint?: string;
  showToken?: boolean;
  lockCredentialWire?: boolean;
};

function httpUrlField(
  raw: string,
  opts: { projectScope?: boolean; customClass?: "local" | "web" },
) {
  const trimmed = raw.trim();
  if (!trimmed) return {};
  const loopbackOnly =
    Boolean(opts.projectScope) || opts.customClass === "local";
  const classified = classifyMcpHttpUrl(trimmed, loopbackOnly);
  if (!classified.ok) {
    return { error: mcpHttpUrlFieldMessage(classified.code) };
  }
  if (opts.customClass === "web" && classified.kind === "loopback") {
    return { error: MCP_SETTINGS_COPY.addCustomLocalHint };
  }
  return {
    hint:
      classified.kind === "loopback"
        ? MCP_SETTINGS_COPY.urlHintLoopback(classified.url)
        : MCP_SETTINGS_COPY.urlHintRemote(classified.url),
  };
}

function McpTransportField(props: {
  draft: Accessor<ConnectionDraft>;
  setDraft: (updater: (d: ConnectionDraft) => ConnectionDraft) => void;
  /** Web custom add is HTTP-only. */
  locked?: "http";
}) {
  return (
    <Show
      when={props.locked !== "http"}
      fallback={
        <DenField label={MCP_SETTINGS_COPY.fieldTransport}>
          <DenInput
            id="mcp-editor-transport"
            data-testid="mcp-editor-transport"
            value={MCP_SETTINGS_COPY.transportHttp}
            disabled
          />
        </DenField>
      }
    >
      <DenField label={MCP_SETTINGS_COPY.fieldTransport}>
        <BrowseSegmented
          class="den-settings-segmented"
          ariaLabel={MCP_SETTINGS_COPY.fieldTransport}
          testId="mcp-editor-transport"
          value={props.draft().transport}
          onChange={(id) => {
            if (id === "stdio" || id === "http") {
              props.setDraft((d) => ({
                ...d,
                transport: id,
              }));
            }
          }}
          options={[
            { id: "http", label: MCP_SETTINGS_COPY.transportHttp },
            { id: "stdio", label: MCP_SETTINGS_COPY.transportStdio },
          ]}
        />
      </DenField>
    </Show>
  );
}

export function McpConnectionFields(props: ConnectionFieldsProps) {
  const urlField = () =>
    httpUrlField(props.draft().url, {
      projectScope: Boolean(props.projectScope),
      customClass: props.customClass,
    });
  const transportLocked = () =>
    props.projectScope || props.customClass === "web"
      ? ("http" as const)
      : undefined;
  const showToken = () =>
    !props.projectScope &&
    props.showToken !== false &&
    props.customClass !== "local";
  return (
    <div class="den-settings-mcp-form" data-testid="mcp-editor-form">
      <Show
        when={props.includeId}
        fallback={
          <McpTransportField
            draft={props.draft}
            setDraft={props.setDraft}
            locked={transportLocked()}
          />
        }
      >
        <div class="den-settings-mcp-form__row">
          <DenField
            label={MCP_SETTINGS_COPY.fieldId}
            hint={MCP_SETTINGS_COPY.fieldIdHint}
          >
            <DenInput
              id="mcp-editor-id"
              data-testid="mcp-editor-id"
              autocomplete="off"
              value={props.idValue?.() ?? ""}
              onInput={(e) => props.onIdChange?.(e.currentTarget.value)}
            />
          </DenField>
          <McpTransportField
            draft={props.draft}
            setDraft={props.setDraft}
            locked={transportLocked()}
          />
        </div>
      </Show>

      <Show when={props.draft().transport === "stdio"}>
        <DenField label={MCP_SETTINGS_COPY.fieldCommand}>
          <DenInput
            id="mcp-editor-command"
            data-testid="mcp-editor-command"
            autocomplete="off"
            value={props.draft().command}
            onInput={(e) =>
              props.setDraft((d) => ({ ...d, command: e.currentTarget.value }))
            }
          />
        </DenField>
        <DenField label={MCP_SETTINGS_COPY.fieldArgs}>
          <DenInput
            id="mcp-editor-args"
            data-testid="mcp-editor-args"
            autocomplete="off"
            value={props.draft().args}
            onInput={(e) =>
              props.setDraft((d) => ({ ...d, args: e.currentTarget.value }))
            }
          />
        </DenField>
        <DenField label={MCP_SETTINGS_COPY.fieldEnv}>
          <textarea
            id="mcp-editor-env"
            class="den-settings-mcp-env"
            data-testid="mcp-editor-env"
            rows={3}
            placeholder={
              props.includeId ? undefined : "Leave blank to keep existing env"
            }
            value={props.draft().envText}
            onInput={(e) =>
              props.setDraft((d) => ({ ...d, envText: e.currentTarget.value }))
            }
          />
        </DenField>
      </Show>

      <Show when={props.draft().transport === "http"}>
        <DenField
          label={MCP_SETTINGS_COPY.fieldUrl}
          hint={urlField().hint}
          error={urlField().error}
        >
          <DenInput
            id="mcp-editor-url"
            data-testid="mcp-editor-url"
            autocomplete="off"
            placeholder={
              props.customClass === "web"
                ? MCP_SETTINGS_COPY.urlPlaceholderWeb
                : MCP_SETTINGS_COPY.urlPlaceholder
            }
            aria-invalid={Boolean(urlField().error)}
            value={props.draft().url}
            onInput={(e) =>
              props.setDraft((d) => ({ ...d, url: e.currentTarget.value }))
            }
          />
        </DenField>
        <Show when={showToken()}>
          <Show when={!props.lockCredentialWire}>
            <DenField label={MCP_SETTINGS_COPY.fieldCredentialWire}>
              <DenSelect
                id="mcp-editor-credential-wire"
                aria-label={MCP_SETTINGS_COPY.fieldCredentialWire}
                data-testid="mcp-editor-credential-wire"
                value={props.draft().credentialWire}
                options={[
                  { value: "bearer", label: MCP_SETTINGS_COPY.wireBearer },
                  { value: "token_token", label: MCP_SETTINGS_COPY.wireTokenToken },
                  { value: "header", label: MCP_SETTINGS_COPY.wireHeader },
                ]}
                onValueChange={(value) =>
                  props.setDraft((d) => ({
                    ...d,
                    credentialWire: value as McpCredentialWire,
                  }))
                }
              />
            </DenField>
            <Show when={props.draft().credentialWire === "header"}>
              <DenField label={MCP_SETTINGS_COPY.fieldCredentialHeader}>
                <DenInput
                  id="mcp-editor-credential-header"
                  data-testid="mcp-editor-credential-header"
                  autocomplete="off"
                  placeholder={MCP_SETTINGS_COPY.headerPlaceholder}
                  value={props.draft().credentialHeader}
                  onInput={(e) =>
                    props.setDraft((d) => ({
                      ...d,
                      credentialHeader: e.currentTarget.value,
                    }))
                  }
                />
              </DenField>
            </Show>
          </Show>
          <DenField
            label={
              props.tokenLabel ??
              (props.draft().credentialWire === "bearer"
                ? MCP_SETTINGS_COPY.fieldBearer
                : MCP_SETTINGS_COPY.fieldToken)
            }
            hint={props.tokenHint ?? MCP_SETTINGS_COPY.fieldTokenHint}
          >
            <DenInput
              id="mcp-editor-token"
              type="password"
              autocomplete="off"
              data-testid="mcp-editor-token"
              value={props.draft().token}
              onInput={(e) =>
                props.setDraft((d) => ({ ...d, token: e.currentTarget.value }))
              }
            />
          </DenField>
        </Show>
      </Show>
    </div>
  );
}
