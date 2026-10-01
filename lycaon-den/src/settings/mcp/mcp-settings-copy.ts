import { CONFIG_DIR_LABEL, CONFIG_DIR_LABEL_DEV } from "../../../shared/brand.ts";
import { overlayRel } from "../../platform/files/overlay-dir.ts";

const mcpConfigDirLabel = import.meta.env.DEV
  ? CONFIG_DIR_LABEL_DEV
  : CONFIG_DIR_LABEL;

/** User-facing MCP settings copy (threat model + connection checks). */
export const MCP_SETTINGS_COPY = {
  title: "MCP providers",
  threatLocal:
    "Local providers run a confined program on this computer, or talk to a loopback URL. Off by default — test the connection before you trust them in sessions.",
  threatWeb:
    "Web providers connect to a remote host over HTTPS, with tool pins and optional Sign in. Off by default — review the host before you enable it.",
  threatDocs: "docs/security.md",
  accessExplainer:
    "Enabled registers the provider's tools. Workflows decide which agents receive all registered tools.",
  pathHintsTitle: "Default config paths",
  pathHintUser: `${mcpConfigDirLabel}/mcp.yaml`,
  pathHintProject: overlayRel("mcp.yaml"),
  emptyProviders:
    "No MCP providers yet. Add from a recipe, or create a custom local or web provider.",
  groupLocal: "Local",
  groupWeb: "Web / hosted",
  testConnection: "Test connection",
  testingConnection: "Testing…",
  connectionResultsTitle: "Connection results",
  connectionResultsDismiss: "Dismiss",
  addProvider: "Add provider",
  providersBack: "Providers",
  addDialogTitle: "Add MCP provider",
  addDialogCustomLocalTitle: "Custom local",
  addDialogCustomWebTitle: "Custom web",
  addPickHint:
    "Choose a known provider, or add a custom local program / loopback URL, or a custom web host.",
  addCustomLocal: "Custom local",
  addCustomLocalHint:
    "A local command (stdio) or a loopback HTTP URL on this computer.",
  addCustomWeb: "Custom web",
  addCustomWebHint:
    "A remote HTTPS MCP host, with a token or Sign in after you add it.",
  projectDeviceManaged:
    "This provider is managed in device Settings. A project may inherit or disable it, but cannot change its connection or enable it here.",
  recipeAdded: "Added",
  recipeDocs: "Docs",
  recipeEnvKeys: (labels: string[]) => `Env after add: ${labels.join(", ")}`,
  pickBack: "Back",
  deleteTitle: "Remove this MCP provider?",
  deleteConfirm:
    "Remove this overlay row? Distro defaults remain if this id ships in the catalog.",
  save: "Save",
  saveConnection: "Save connection",
  cancel: "Cancel",
  delete: "Remove",
  connectionHeading: "Connection",
  tools: "Tools",
  toolsEmpty: "No tools discovered yet. Enable the provider and resync.",
  toolsUnavailable: "Couldn\u2019t list tools. Open again or resync to retry.",
  toolsLoading: "Loading tools…",
  toolsDisabled: "Enable this provider to discover its tools.",
  resync: "Resync",
  resyncing: "Resyncing…",
  columnProvider: "Provider",
  columnEnabled: "Enabled",
  toolLoadingAlways: "Always load tool definitions",
  toolLoadingHint:
    "By default, tool definitions load as needed through request_tools. Always load sends this provider’s definitions on every model call.",
  columnStatus: "Status",
  columnDetail: "Detail",
  columnTransport: "Transport",
  columnSource: "Source",
  statusOk: "ok",
  statusDisabled: "disabled",
  statusError: "error",
  unnamedOverlayRow: "Unnamed overlay row",
  rejectedLabel: "Rejected",
  fieldId: "Name",
  fieldIdHint: "Shown in Settings and in tool names.",
  fieldTransport: "Transport",
  fieldCommand: "Command",
  fieldArgs: "Args (space-separated)",
  fieldUrl: "URL",
  urlPlaceholder: "http://127.0.0.1:8765/mcp",
  urlPlaceholderWeb: "https://mcp.example.com/mcp",
  urlHintLoopback: (canonical: string) =>
    `Local provider on this computer. Saved as ${canonical}.`,
  urlHintRemote: (canonical: string) =>
    `Remote provider — connects over HTTPS. Saved as ${canonical}.`,
  urlErrorEmpty: "Enter a URL.",
  urlErrorInvalid:
    "Enter a URL, for example http://127.0.0.1:8765/mcp.",
  urlErrorRemoteHTTP:
    "Remote providers must use HTTPS. Use an https:// URL, or a loopback address if the provider is on this computer.",
  urlErrorProjectRemote:
    "A project can only add a provider on this computer (127.0.0.1, localhost, or ::1).",
  fieldBearer: "Bearer token",
  fieldToken: "Token",
  fieldTokenHint:
    "Static token the provider accepts. Sign in is for providers that register this app automatically.",
  fieldCredentialWire: "Send token as",
  wireBearer: "Authorization: Bearer",
  wireTokenToken: "Authorization: Token token=",
  wireHeader: "Named header",
  fieldCredentialHeader: "Header name",
  headerPlaceholder: "X-Api-Key",
  fieldEnv: "Env (KEY=value per line)",
  tokenConfigured: "Token configured",
  headersConfigured: "Headers configured",
  envConfigured: "Env configured",
  signedIn: "Signed in",
  needsAuth: "Needs sign-in",
  signIn: "Sign in",
  signOut: "Sign out",
  oauthCodeTitle: "Complete sign-in",
  oauthWaitingHint:
    "Finish signing in in your browser. This closes on its own when the provider confirms.",
  oauthManualToggle: "Enter the code manually",
  oauthCodeHint:
    "Only needed if the browser could not return to the app. Paste the authorization code from the redirect.",
  fieldOAuthCode: "Authorization code",
  oauthComplete: "Complete",
  transportStdio: "stdio",
  transportHttp: "HTTP",
  followingSummary: (enabledCount: number, total: number) =>
    `${enabledCount} of ${total} enabled`,
} as const;

export function mcpHttpUrlFieldMessage(
  code: "empty" | "invalid_url" | "remote_requires_https" | "project_remote_forbidden",
): string {
  switch (code) {
    case "empty":
      return MCP_SETTINGS_COPY.urlErrorEmpty;
    case "invalid_url":
      return MCP_SETTINGS_COPY.urlErrorInvalid;
    case "remote_requires_https":
      return MCP_SETTINGS_COPY.urlErrorRemoteHTTP;
    case "project_remote_forbidden":
      return MCP_SETTINGS_COPY.urlErrorProjectRemote;
  }
}
