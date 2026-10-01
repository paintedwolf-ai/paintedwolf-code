import type { McpCredentialWire, McpProvider, UpdateMcpProviderRequest } from "../../../api/types.ts";
import { applyCredentialWire, normalizeCredentialWire } from "../../../settings/mcp/mcp-provider-model.ts";

export type ConnectionDraft = {
  transport: "stdio" | "http";
  command: string;
  args: string;
  url: string;
  token: string;
  credentialWire: McpCredentialWire;
  credentialHeader: string;
  envText: string;
};

export type AddDraft = ConnectionDraft & {
  id: string;
};

export function emptyConnectionDraft(): ConnectionDraft {
  return {
    transport: "http",
    command: "",
    args: "",
    url: "",
    token: "",
    credentialWire: "bearer",
    credentialHeader: "",
    envText: "",
  };
}

export function emptyAddDraft(): AddDraft {
  return { id: "", ...emptyConnectionDraft() };
}

export function draftFromProvider(provider: McpProvider): ConnectionDraft {
  const transport =
    provider.transport === "stdio" || provider.command
      ? ("stdio" as const)
      : ("http" as const);
  return {
    transport,
    command: provider.command ?? "",
    args: (provider.args ?? []).join(" "),
    url: provider.url ?? "",
    token: "",
    credentialWire: normalizeCredentialWire(provider.credential_wire),
    credentialHeader: provider.credential_header ?? "",
    envText: "",
  };
}

export function parseEnvText(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const eq = trimmed.indexOf("=");
    if (eq <= 0) continue;
    out[trimmed.slice(0, eq)] = trimmed.slice(eq + 1);
  }
  return Object.keys(out).length ? out : undefined;
}

export function connectionBodyFromDraft(
  d: ConnectionDraft,
  httpURL?: string,
  lockCredentialWire?: boolean,
): UpdateMcpProviderRequest {
  const body: UpdateMcpProviderRequest = {};
  if (d.transport === "stdio") {
    body.command = d.command.trim();
    body.args = d.args
      .trim()
      .split(/\s+/)
      .filter(Boolean);
    if (d.envText.trim()) {
      body.env = parseEnvText(d.envText) ?? {};
    }
  } else {
    body.url = httpURL ?? d.url.trim();
    if (d.token.trim()) body.token = d.token.trim();
    if (!lockCredentialWire) {
      applyCredentialWire(body, d.credentialWire, d.credentialHeader);
    }
  }
  return body;
}
