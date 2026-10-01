import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { expect } from "@playwright/test";
import type { CreateMcpProviderRequest } from "../src/api/types.ts";
import { apiConfig, expectShellReady, webE2e } from "./helpers.ts";

webE2e("web MCP sign-in, sign-out, and replacement credentials reach the authenticated server", async ({ page, request }) => {
  let origin = "";
  const tokens: string[] = [];
  const authenticated: string[] = [];
  const exchanges: URLSearchParams[] = [];
  const server = createServer((req, res) => {
    void (async () => {
      const url = new URL(req.url ?? "/", origin);
      res.setHeader("Content-Type", "application/json");
      if (url.pathname.startsWith("/.well-known/oauth-protected-resource")) {
        res.end(JSON.stringify({ resource: `${origin}/mcp`, authorization_servers: [origin], scopes_supported: ["mcp"] }));
        return;
      }
      if (url.pathname.startsWith("/.well-known/oauth-authorization-server")) {
        res.end(JSON.stringify({
          issuer: origin, authorization_endpoint: `${origin}/authorize`, token_endpoint: `${origin}/token`,
          registration_endpoint: `${origin}/register`, code_challenge_methods_supported: ["S256"],
          response_types_supported: ["code"], grant_types_supported: ["authorization_code"],
        }));
        return;
      }
      if (url.pathname === "/register") {
        res.end(JSON.stringify({ client_id: "lifecycle-fixture", token_endpoint_auth_method: "none" }));
        return;
      }
      let body = "";
      for await (const chunk of req) body += chunk.toString();
      if (url.pathname === "/token") {
        exchanges.push(new URLSearchParams(body));
        const token = `disposable-mcp-token-${tokens.length + 1}`;
        tokens.push(token);
        res.end(JSON.stringify({ access_token: token, token_type: "Bearer", expires_in: 3600 }));
        return;
      }
      const bearer = req.headers.authorization?.replace(/^Bearer /, "");
      if (!bearer || bearer !== tokens[tokens.length - 1]) {
        res.statusCode = 401;
        res.setHeader("WWW-Authenticate", `Bearer resource_metadata="${origin}/.well-known/oauth-protected-resource"`);
        res.end("{}");
        return;
      }
      authenticated.push(bearer);
      if (req.method !== "POST") {
        res.statusCode = 405;
        res.end("{}");
        return;
      }
      const rpc = JSON.parse(body) as { id?: number | string; method: string; params?: { protocolVersion?: string } };
      if (rpc.id === undefined) {
        res.statusCode = 202;
        res.end();
        return;
      }
      const result = rpc.method === "initialize"
        ? { protocolVersion: rpc.params?.protocolVersion, capabilities: { tools: {} }, serverInfo: { name: "auth-fixture", version: "1.0.0" } }
        : { tools: [{ name: "echo", description: "Return disposable fixture text", inputSchema: { type: "object", properties: { value: { type: "string" } }, required: ["value"] } }] };
      res.end(JSON.stringify({ jsonrpc: "2.0", id: rpc.id, result }));
    })().catch((error: unknown) => {
      res.statusCode = 500;
      res.end(JSON.stringify({ error: String(error) }));
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const id = `sign-in-lifecycle-${Date.now()}`;
  const providerURL = `${apiUrl}/v1/mcp/providers/${id}`;
  try {
    const created = await request.post(`${apiUrl}/v1/mcp/providers`, { headers, data: { source: "custom", id, url: `${origin}/mcp`, enabled: false } satisfies CreateMcpProviderRequest });
    expect(created.status()).toBe(201);
    await page.goto("/");
    await expectShellReady(page);
    await page.getByRole("button", { name: "Settings", exact: true }).click();
    await page.getByTestId("settings-nav-mcp").click();
    await page.getByTestId(`mcp-provider-${id}`).click();

    for (const mode of ["redirect", "manual"] as const) {
      const starting = page.waitForResponse((response) => response.url().endsWith(`/providers/${id}/oauth/start`));
      await page.getByTestId(`mcp-oauth-signin-${id}`).click();
      const started = await (await starting).json() as { state: string; redirect_uri: string };
      await page.getByRole("alertdialog", { name: "Open external link" }).getByRole("button", { name: "Cancel", exact: true }).click();
      await expect(page.getByRole("dialog", { name: "Complete sign-in" })).toBeVisible();
      if (mode === "redirect") {
        const callback = new URL(started.redirect_uri);
        callback.searchParams.set("state", started.state);
        callback.searchParams.set("code", "redirect-code");
        expect((await request.get(callback.href)).ok()).toBe(true);
      } else {
        await page.getByTestId("mcp-oauth-manual").click();
        await page.getByTestId("mcp-oauth-code").fill("manual-code");
        await page.getByTestId(`mcp-oauth-complete-${id}`).click();
      }
      await expect(page.getByRole("dialog", { name: "Complete sign-in" })).toHaveCount(0);
      await expect(page.getByTestId(`mcp-oauth-signout-${id}`)).toBeVisible();
      if (mode === "redirect") await page.getByTestId(`mcp-enable-${id}`).click();
      expect((await request.post(`${providerURL}/refresh`, { headers })).ok()).toBe(true);
      await expect.poll(() => authenticated.includes(tokens[tokens.length - 1]!)).toBe(true);
      const inventory = await request.get(`${apiUrl}/v1/mcp/providers`, { headers });
      const inventoryText = await inventory.text();
      for (const secret of tokens) expect(inventoryText).not.toContain(secret);
      const tools = await request.get(`${providerURL}/tools`, { headers });
      expect(tools.ok()).toBe(true);
      expect(await tools.text()).toContain("echo");
      const latest = tokens[tokens.length - 1]!;
      await page.getByTestId(`mcp-oauth-signout-${id}`).click();
      await expect(page.getByTestId(`mcp-oauth-signin-${id}`)).toBeVisible();
      const afterSignOut = authenticated.length;
      await request.post(`${providerURL}/refresh`, { headers });
      expect(authenticated.slice(afterSignOut)).not.toContain(latest);
    }
    expect(tokens).toHaveLength(2);
    expect(exchanges.map((entry) => entry.get("code"))).toEqual(["redirect-code", "manual-code"]);
    for (const exchange of exchanges) {
      expect(exchange.get("code_verifier")?.length).toBeGreaterThan(20);
      expect(exchange.get("resource")).toBe(`${origin}/mcp`);
    }
  } finally {
    await request.delete(providerURL, { headers });
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
});
