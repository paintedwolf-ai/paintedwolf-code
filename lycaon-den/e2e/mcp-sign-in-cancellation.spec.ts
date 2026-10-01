import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { expect } from "@playwright/test";
import type { CreateMcpProviderRequest } from "../src/api/types.ts";
import { apiConfig, expectShellReady, webE2e } from "./helpers.ts";

webE2e("canceling MCP sign-in closes its callback and preserves a newer attempt", async ({ page, request }) => {
  let origin = "";
  let tokenExchanges = 0;
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? "/", origin);
    res.setHeader("Content-Type", "application/json");
    if (url.pathname.startsWith("/.well-known/oauth-protected-resource")) {
      res.end(JSON.stringify({ resource: `${origin}/mcp`, authorization_servers: [origin], scopes_supported: ["mcp"] }));
    } else if (url.pathname.startsWith("/.well-known/oauth-authorization-server")) {
      res.end(JSON.stringify({
        issuer: origin, authorization_endpoint: `${origin}/authorize`, token_endpoint: `${origin}/token`,
        registration_endpoint: `${origin}/register`, code_challenge_methods_supported: ["S256"],
        response_types_supported: ["code"], grant_types_supported: ["authorization_code"],
      }));
    } else if (url.pathname === "/register") {
      res.end(JSON.stringify({ client_id: "browser-fixture", token_endpoint_auth_method: "none" }));
    } else if (url.pathname === "/token") {
      tokenExchanges += 1;
      res.end(JSON.stringify({ access_token: "must-not-be-issued", token_type: "Bearer" }));
    } else {
      res.statusCode = 401;
      res.setHeader("WWW-Authenticate", `Bearer resource_metadata="${origin}/.well-known/oauth-protected-resource"`);
      res.end("{}");
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const id = `sign-in-cancel-${Date.now()}`;
  const providerURL = `${apiUrl}/v1/mcp/providers/${id}`;
  try {
    const created = await request.post(`${apiUrl}/v1/mcp/providers`, {
      headers, data: { source: "custom", id, url: `${origin}/mcp`, enabled: false } satisfies CreateMcpProviderRequest,
    });
    expect(created.status()).toBe(201);
    await page.goto("/");
    await expectShellReady(page);
    await page.getByRole("button", { name: "Settings", exact: true }).click();
    await page.getByTestId("settings-nav-mcp").click();
    await page.getByTestId(`mcp-provider-${id}`).click();

    const start = async () => {
      const response = page.waitForResponse((candidate) => candidate.url().endsWith(`/providers/${id}/oauth/start`));
      await page.getByTestId(`mcp-oauth-signin-${id}`).click();
      const started = await (await response).json() as { state: string; redirect_uri: string };
      await page.getByRole("alertdialog", { name: "Open external link" })
        .getByRole("button", { name: "Cancel", exact: true }).click();
      await expect(page.getByRole("dialog", { name: "Complete sign-in" })).toBeVisible();
      return started;
    };
    const cancel = async () => {
      await page.getByRole("dialog", { name: "Complete sign-in" })
        .getByRole("button", { name: "Cancel", exact: true }).click();
      await expect(page.getByRole("dialog", { name: "Complete sign-in" })).toHaveCount(0);
    };
    const first = await start();
    await cancel();
    await expect.poll(async () => {
      try {
        await request.get(first.redirect_uri, { timeout: 1_000 });
        return false;
      } catch {
        return true;
      }
    }).toBe(true);
    const late = await request.post(`${providerURL}/oauth/complete`, {
      headers, data: { state: first.state, code: "canceled-code" },
    });
    expect(late.ok()).toBe(false);

    const second = await start();
    expect(second.state).not.toBe(first.state);
    expect((await request.post(`${providerURL}/oauth/cancel`, { headers, data: { state: first.state } })).status()).toBe(204);
    expect((await request.get(second.redirect_uri)).status()).toBe(400);
    await cancel();
    expect(tokenExchanges).toBe(0);
  } finally {
    await request.delete(providerURL, { headers });
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
});
