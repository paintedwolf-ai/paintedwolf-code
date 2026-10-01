import { MODELS_SETTINGS_COPY } from "./models-settings-copy.ts";

const ACCOUNT_PLACEHOLDER = "YOUR_ACCOUNT_ID";

/** Edit the account segment in place, preserving custom origins and URL options. */
export function cloudflareAccountEndpoint(baseUrl: string) {
  try {
    const url = new URL(baseUrl.trim());
    const match = /\/accounts\/([^/]*)\/ai\/v1\/?$/.exec(url.pathname);
    if (!match) return null;
    const rawId = decodeURIComponent(match[1]!).trim();
    const accountId = rawId.toUpperCase() === ACCOUNT_PLACEHOLDER ? "" : rawId;
    return {
      accountId,
      withAccountId(value: string): string {
        url.pathname = url.pathname.replace(
          /\/accounts\/[^/]*\/ai\/v1(\/?)$/,
          `/accounts/${encodeURIComponent(value.trim())}/ai/v1$1`,
        );
        return url.toString();
      },
    };
  } catch {
    return null;
  }
}

/** Form validation only; saved provider readiness comes from the host. */
export function providerEndpointError(kind: string, baseUrl: string): string | undefined {
  if (kind !== "cloudflare-workers-ai") return undefined;
  const endpoint = cloudflareAccountEndpoint(baseUrl);
  if (!endpoint) return MODELS_SETTINGS_COPY.cloudflareEndpointInvalid;
  if (!endpoint.accountId.trim()) return MODELS_SETTINGS_COPY.cloudflareAccountIdRequired;
  return undefined;
}
