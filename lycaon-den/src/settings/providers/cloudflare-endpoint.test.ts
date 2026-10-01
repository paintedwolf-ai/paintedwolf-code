import { describe, expect, it } from "vitest";
import { cloudflareAccountEndpoint, providerEndpointError } from "./cloudflare-endpoint.ts";

describe("Cloudflare endpoint editing", () => {
  it("preserves a custom origin, prefix, query and trailing slash when replacing the account", () => {
    const endpoint = cloudflareAccountEndpoint("https://proxy.example/prefix/accounts/old/ai/v1/?option=1");
    expect(endpoint?.accountId).toBe("old");
    expect(endpoint?.withAccountId(" new-account ")).toBe(
      "https://proxy.example/prefix/accounts/new-account/ai/v1/?option=1",
    );
  });

  it.each(["YOUR_ACCOUNT_ID", "your_account_id", "%20YOUR_ACCOUNT_ID%20", ""])(
    "requires an account when the endpoint contains %s",
    (value) => {
      const url = `https://api.cloudflare.com/client/v4/accounts/${value}/ai/v1`;
      expect(cloudflareAccountEndpoint(url)?.accountId).toBe("");
      expect(providerEndpointError("cloudflare-workers-ai", url)).toBeTruthy();
    },
  );

  it.each(["not a URL", "https://api.cloudflare.com/v1", "https://example.com/accounts/%/ai/v1"])(
    "rejects an endpoint the account editor cannot parse: %s",
    (url) => {
      expect(cloudflareAccountEndpoint(url)).toBeNull();
      expect(providerEndpointError("cloudflare-workers-ai", url)).toBeTruthy();
    },
  );

  it("does not apply Cloudflare validation to other provider kinds", () => {
    expect(providerEndpointError("openai-compatible", "http://localhost:1234/v1")).toBeUndefined();
  });
});
