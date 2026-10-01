import { spawn } from "node:child_process";
import { randomUUID } from "node:crypto";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect } from "@playwright/test";
import { webE2e } from "./helpers.ts";

const repoRoot = fileURLToPath(new URL("../../", import.meta.url));

async function freePort(): Promise<number> {
  const listener = createServer();
  await new Promise<void>((resolve, reject) => {
    listener.once("error", reject);
    listener.listen(0, "127.0.0.1", resolve);
  });
  const address = listener.address();
  if (!address || typeof address === "string") throw new Error("Missing fixture address");
  await new Promise<void>((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()));
  return address.port;
}

webE2e("sidecar CORS preserves export metadata and staged-recovery errors", async ({ page, request, baseURL, browserName }) => {
  if (browserName === "chromium") {
    await page.context().grantPermissions(["local-network-access"], { origin: "http://tauri.localhost" });
  }
  const stateDir = process.env.LYCAON_E2E_STATE_DIR;
  if (!stateDir) throw new Error("The CORS fixture requires a prepared harness runtime");
  const dir = await mkdtemp(path.join(tmpdir(), "sidecar-cors-"));
  const configDir = path.join(dir, "config");
  const projectDir = path.join(dir, "project");
  await mkdir(configDir);
  await mkdir(projectDir);
  // Cross the 100,000-hit export cap while staying below the 1 MiB search-file limit.
  await writeFile(path.join(projectDir, "matches.txt"), "exporthit\n".repeat(100_001));
  const port = await freePort();
  const apiUrl = `http://127.0.0.1:${port}`;
  const token = randomUUID();
  const child = spawn(path.join(stateDir, "runtime/sidecar"), ["serve", "--db", path.join(configDir, "store.db")], {
    cwd: repoRoot,
    env: {
      ...process.env,
      LYCAON_ADDR: `127.0.0.1:${port}`,
      LYCAON_CONFIG_DIR: configDir,
      LYCAON_API_TOKEN: token,
      LYCAON_LLM_MOCK: "1",
      LYCAON_LLM_MANUAL: "0",
      LYCAON_DEV_CORS: "0",
      LYCAON_ENGINE_ROOT: path.join(repoRoot, "lycaon-den/src-tauri/engine-root"),
    },
    stdio: ["ignore", "ignore", "pipe"],
  });
  let diagnostics = "";
  child.stderr.on("data", (data: Buffer) => { diagnostics = (diagnostics + data.toString()).slice(-16_384); });
  let spawnError: Error | undefined;
  const exited = new Promise<void>((resolve) => {
    child.once("exit", () => resolve());
    child.once("error", (error) => { spawnError = error; resolve(); });
  });
  try {
    await expect.poll(async () => {
      if (spawnError) throw spawnError;
      if (child.exitCode !== null) throw new Error(`Sidecar exited: ${diagnostics}`);
      return request.get(`${apiUrl}/health`).then((response) => response.ok()).catch(() => false);
    }, { timeout: 60_000 }).toBe(true);
    const headers = { Authorization: `Bearer ${token}` };
    const created = await request.post(`${apiUrl}/v1/projects`, { headers, data: { name: "Transport fixture", roots: [{ path: projectDir }] } });
    expect(created.ok(), await created.text()).toBeTruthy();
    const project = await created.json() as { id: string };
    await expect.poll(async () => {
      const response = await request.post(`${apiUrl}/v1/search`, {
        headers, data: { query: "kind:code exporthit", origin_project_id: project.id },
      });
      if (!response.ok()) return false;
      const result = await response.json() as { hits: unknown[] };
      return result.hits.length > 0;
    }, { timeout: 30_000 }).toBe(true);

    // API requests reach the isolated sidecar directly from an allowed desktop origin.
    await page.route("http://tauri.localhost/**", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/cors-check") {
        await route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Sidecar transport check</title>" });
        return;
      }
      const response = await route.fetch({ url: new URL(url.pathname + url.search, baseURL).href });
      await route.fulfill({ response });
    });
    await page.goto("http://tauri.localhost/cors-check");
    const exported = await page.evaluate(async ({ apiUrl, token, projectId, moduleUrl }) => {
      const { createLycaonClient } = await import(/* @vite-ignore */ moduleUrl) as typeof import("../src/api/client-impl.ts");
      const client = createLycaonClient({ baseUrl: apiUrl, apiToken: token });
      const result = await client.exportSearchResults("kind:code exporthit", "jsonl", projectId);
      return { filename: result.filename, truncated: result.truncated, rows: (await result.blob.text()).trim().split("\n").length };
    }, { apiUrl, token, projectId: project.id, moduleUrl: "http://tauri.localhost/src/api/client-impl.ts" });
    expect(exported.truncated).toBe(true);
    expect(exported.filename).not.toBe("search-export.jsonl");
    expect(exported.filename).toMatch(/\.jsonl$/);
    expect(exported.rows).toBe(100_001);

    const recovery = await page.evaluate(async ({ apiUrl, token, moduleUrl }) => {
      const { lycaonJson, LycaonApiError } = await import(/* @vite-ignore */ moduleUrl) as typeof import("../src/api/http.ts");
      const connection = { baseUrl: apiUrl, apiToken: token };
      await lycaonJson(connection, "/v1/store/reset", { method: "POST" });
      try {
        // A fresh route forces preflight after staging, as a reconnect would.
        await lycaonJson(connection, "/v1/backup/restore/recovery-snapshot", { method: "POST" });
        return { code: "unexpected_success", status: 200, health: false };
      } catch (error) {
        if (!(error instanceof LycaonApiError)) throw error;
        return { code: error.code, status: error.status, health: (await fetch(`${apiUrl}/health`)).ok };
      }
    }, { apiUrl, token, moduleUrl: "http://tauri.localhost/src/api/http.ts" });
    expect(recovery).toEqual({ code: "backup_restore_pending", status: 409, health: true });
  } finally {
    if (child.exitCode === null) child.kill("SIGTERM");
    const timer = setTimeout(() => { if (child.exitCode === null) child.kill("SIGKILL"); }, 15_000);
    try { await exited; } finally { clearTimeout(timer); }
    await rm(dir, { recursive: true, force: true });
  }
});
