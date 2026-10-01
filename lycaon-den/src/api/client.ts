// The authenticated host client composes its capability contracts.
import type { HostClient } from "./http-capabilities/host.ts";
import type { SessionsClient } from "./http-capabilities/sessions.ts";
import type { WorkflowsClient } from "./http-capabilities/workflows.ts";
import type { ProjectsClient } from "./http-capabilities/projects.ts";
import type { SourceClient } from "./http-capabilities/source.ts";
import type { SourceHistoryClient } from "./http-capabilities/source-history.ts";
import type { DocumentsClient } from "./http-capabilities/documents.ts";
import type { SearchClient } from "./http-capabilities/search.ts";
import type { GitClient } from "./http-capabilities/git.ts";
import type { SecurityClient } from "./http-capabilities/security.ts";
import type { SettingsClient } from "./http-capabilities/settings.ts";
import type { ExtensionsClient } from "./http-capabilities/extensions.ts";
import type { McpClient } from "./http-capabilities/mcp.ts";
import type { StorageClient } from "./http-capabilities/storage.ts";
import type { SourceViewsClient } from "./source-views-client.ts";

export interface LycaonClient extends
  HostClient,
  SessionsClient,
  WorkflowsClient,
  ProjectsClient,
  SourceClient,
  SourceHistoryClient,
  DocumentsClient,
  SearchClient,
  GitClient,
  SecurityClient,
  SettingsClient,
  ExtensionsClient,
  McpClient,
  StorageClient,
  SourceViewsClient {}
