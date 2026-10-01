import type { CatastrophicDetail } from "../api/types.ts";
import type { HealthResponse } from "./connection/backend.ts";
import { DEN_VERSION } from "./desktop/den-version.ts";
import { lastSeenHostVersion, lastSeenSchemaVersion } from "./connection/health.ts";
import { tauriPlatform } from "./runtime.ts";

/** Non-sensitive stop facts remain available when the engine is unreachable. */
export function stopFactsLine(
  detail?: CatastrophicDetail,
  health?: HealthResponse | null,
): string {
  const parts: string[] = [];

  const app = detail?.app_version ?? health?.version ?? lastSeenHostVersion();
  if (app) parts.push(`engine ${app}`);
  parts.push(`app ${DEN_VERSION}`);

  const expectedSchema =
    detail?.schema_version ?? health?.schema_version ?? lastSeenSchemaVersion();
  const storedSchema =
    health?.status === "recovery" ? health.store_schema_version : undefined;
  if (
    expectedSchema != null &&
    storedSchema != null &&
    expectedSchema !== storedSchema
  ) {
    parts.push(`expected schema ${expectedSchema}`, `found schema ${storedSchema}`);
  } else {
    const schema = storedSchema ?? expectedSchema;
    if (schema != null) parts.push(`store schema ${schema}`);
  }

  const platform = detail?.os_name ?? tauriPlatform();
  if (platform) {
    parts.push(detail?.os_version ? `${platform} ${detail.os_version}` : platform);
  }
  if (detail?.os_floor) parts.push(`needs ${detail.os_floor}+`);
  if (detail?.arch) parts.push(detail.arch);
  if (detail?.probe_id) parts.push(`probe ${detail.probe_id}`);

  return parts.join(" · ");
}
