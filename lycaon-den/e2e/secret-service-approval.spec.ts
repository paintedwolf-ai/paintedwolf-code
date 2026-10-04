import { randomUUID } from "node:crypto";
import { expect, test, type APIRequestContext } from "@playwright/test";
import { managedSecretId } from "../src/api/managed-secret-reference.ts";
import type { ApprovalGrantsResponse, ManagedSecret, ManagedSecretUseList } from "../src/api/types.ts";
import {
  activateProject, apiConfig, apiFindHarnessProject, apiPostPrompt, fillSolidControl,
  liveChatStage, openNewChatSession,
  settledChatSessionId, waitHarnessConnected, webE2e,
} from "./helpers.ts";
import { startSecretService } from "./secret-service-fixture.ts";
import { manualHarnessTools } from "./manual-harness-tools.ts";

/** Resolves true when the call waits on an unconfined-run card, false once it has a result. */
async function awaitUnconfinedRun(request: APIRequestContext, sessionId: string, callId: string) {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  let unconfined: boolean | undefined;
  await expect.poll(async () => {
    const checkpoints = await request.get(`${apiUrl}/v1/sessions/${sessionId}/checkpoints`, { headers });
    expect(checkpoints.ok(), await checkpoints.text()).toBeTruthy();
    const { checkpoints: pending } = await checkpoints.json() as {
      checkpoints: Array<{ status: string; tool_approval?: { tool_call_id?: string; plan?: { presentation?: { gate?: string } } } }>;
    };
    if (pending.some((checkpoint) => checkpoint.status === "pending" && checkpoint.tool_approval?.tool_call_id === callId &&
      checkpoint.tool_approval.plan?.presentation?.gate === "unobserved_channel")) {
      return unconfined = true;
    }
    const messages = await request.get(`${apiUrl}/v1/sessions/${sessionId}/messages`, { headers });
    expect(messages.ok(), await messages.text()).toBeTruthy();
    const { messages: rows } = await messages.json() as { messages: Array<{ tool_result?: { tool_call_id?: string } }> };
    if (rows.some((message) => message.tool_result?.tool_call_id === callId)) return unconfined = false;
    return undefined;
  }, { timeout: 45_000 }).not.toBeUndefined();
  return unconfined!;
}

webE2e("managed service approval shows recipients and a stable chat primary", async ({ page, request }) => {
  await page.goto("/");
  await waitHarnessConnected(page);
  const project = await apiFindHarnessProject(request);
  await activateProject(page, project.id);
  const sessionId = await settledChatSessionId(page);
  const { apiUrl, token } = apiConfig();
  const injected = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      session_id: sessionId, command: "configure-service --password <protected>",
      secret: {
        surface: "command", destination: "Processes in this chat with mediated network access",
        managed: true, names: ["Service password"],
        recipients: [
          { id: "process", label: "Processes in this chat with mediated network access", surface: "command", kind: "process" },
          { id: "service", label: "http://127.0.0.1:8080", surface: "http_request", kind: "service" },
        ],
      },
    },
  });
  expect(injected.ok(), await injected.text()).toBeTruthy();
  const { checkpoint_id: checkpointId } = await injected.json() as { checkpoint_id: string };
  const card = () => liveChatStage(page).locator(`[data-testid="tool-approval-card"][data-checkpoint-id="${checkpointId}"]`);
  await expect(card()).toBeVisible();
  await expect(card().getByTestId("approval-secret-names")).toContainText("Service password");
  await expect(card().getByTestId("approval-location-destination")).toHaveText([
    "Processes in this chat with mediated network access", "http://127.0.0.1:8080",
  ]);
  await expect(card().getByTestId("approval-approve-primary")).toContainText("Send for this chat");
  await page.reload();
  await waitHarnessConnected(page);
  await expect(card()).toBeVisible();
  await expect(card().getByTestId("approval-approve-primary")).toContainText("Send for this chat");
  await expect(page.getByTestId("shell-workspace-veil")).toHaveAttribute("aria-hidden", "true", { timeout: 30_000 });
  await card().screenshot({ path: "/tmp/secret-service-approval.png", animations: "disabled" });
  await card().getByTestId("approval-approve-primary").click();
  await expect(card()).toHaveCount(0);
});

webE2e("managed service setup approval covers live requests until revoked", async ({ page, request }) => {
  test.setTimeout(300_000);
  page.setDefaultTimeout(15_000);
  const service = await startSecretService();
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  let sessionId = "";
  const browserErrors: string[] = [];
  page.on("pageerror", (error) => browserErrors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") browserErrors.push(message.text());
  });
  try {
    await page.goto("/");
    await waitHarnessConnected(page);
    const project = await apiFindHarnessProject(request);
    await activateProject(page, project.id);
    sessionId = await openNewChatSession(page);
    const posture = await request.patch(`${apiUrl}/v1/settings/approvals`, {
      headers, data: { approval_posture: "balanced" },
    });
    expect(posture.ok(), await posture.text()).toBeTruthy();
    const name = `Live service password ${randomUUID().slice(0, 8)}`;
    const manual = await request.post(`${apiUrl}/harness/llm/auto`, { headers, data: { enabled: false } });
    expect(manual.ok(), await manual.text()).toBeTruthy();
    const prompt = await apiPostPrompt(request, sessionId, { text: "Configure the local test service and verify authenticated requests." });
    expect(prompt.ok(), await prompt.text()).toBeTruthy();
    const driver = manualHarnessTools(request, sessionId);
    await driver.loadTools("secret_generate", "command", "http_request", "update_progress");
    await driver.completed(await driver.invoke("update_progress", {
      content: "## Progress\n- [ ] Configure the service\n- [ ] Verify authenticated requests\n",
    }));
    // A person-held value needs desktop presence; a project-scoped generated value asks through the ordinary card.
    await driver.completed(await driver.invoke("secret_generate", {
      name, purpose: "Live service verification", scope: "project", format: "alphanumeric",
    }));
    const listed = await request.get(`${apiUrl}/v1/projects/${project.id}/secrets`, { headers });
    expect(listed.ok(), await listed.text()).toBeTruthy();
    const secret = (await listed.json() as { secrets: ManagedSecret[] }).secrets.find((entry) => entry.name === name);
    expect(secret).toMatchObject({ origin: "generated", scope: "project" });
    const secretId = managedSecretId(secret!.reference);
    expect(secretId).toBeTruthy();
    const capability = { loopback_connect: { ports: [service.port] } };
    const python = [
      "import http.client, os",
      `connection = http.client.HTTPConnection("127.0.0.1", ${service.port})`,
      'connection.request("POST", "/configure", os.environ["SERVICE_PASSWORD"])',
      "assert connection.getresponse().status == 204",
      'print("service configured")',
    ].join("; ");
    const setup = await driver.invoke("command", {
      command: "/usr/bin/python3 -", stdin: python, cwd: ".",
      env: { SERVICE_PASSWORD: secret!.reference }, secret_use: { services: [service.origin] },
      capability_request: capability,
    });
    const cards = () => liveChatStage(page).getByTestId("tool-approval-card");
    await expect(cards()).toHaveCount(1, { timeout: 30_000 });
    await expect(cards().getByTestId("approval-secret-names")).toContainText(name);
    await expect(cards().getByTestId("approval-location-destination")).toContainText([service.origin]);
    await expect(cards().getByTestId("approval-approve-primary")).toContainText("Allow for this chat");
    expect(service.configured()).toBe(false);
    await expect(page.getByTestId("shell-workspace-veil")).toHaveAttribute("aria-hidden", "true", { timeout: 30_000 });
    await cards().screenshot({ path: "/tmp/secret-service-live-approval.png", animations: "disabled" });
    await cards().getByTestId("approval-approve-primary").click();
    // A host without a command sandbox (Linux) asks once more to run the setup unconfined.
    if (await awaitUnconfinedRun(request, sessionId, setup)) {
      await expect(cards()).toHaveCount(1);
      await expect(cards().getByTestId("approval-secret-names")).toHaveCount(0);
      await cards().getByTestId("approval-approve-primary").click();
    }
    const setupOutput = await driver.completed(setup);
    const setupResult = JSON.parse(setupOutput.slice(setupOutput.indexOf("{"))) as { ok: boolean; tail: string };
    expect(setupResult.ok, setupResult.tail).toBe(true);
    expect(setupResult.tail).toContain("service configured");
    expect(service.configured()).toBe(true);
    await expect(cards()).toHaveCount(0);
    await expect(page.locator("body")).not.toContainText(service.password());

    const httpArgs = {
      url: `${service.origin}/data`, auth: { scheme: "basic", username: "test", password: secret!.reference },
      capability_request: capability,
    };
    for (let index = 0; index < 3; index++) {
      const call = await driver.invoke("http_request", httpArgs);
      expect(await driver.completed(call)).toContain("authenticated");
      expect(service.authenticatedRequests()).toBe(index + 1);
      await expect(cards()).toHaveCount(0);
    }

    const grantsResponse = await request.get(`${apiUrl}/v1/approval-grants`, { headers });
    expect(grantsResponse.ok(), await grantsResponse.text()).toBeTruthy();
    const { grants } = await grantsResponse.json() as ApprovalGrantsResponse;
    expect(grants.filter((item) => item.chat_session_id === sessionId && item.category === "loopback_connect")).toHaveLength(1);
    const grant = grants.find((item) => item.chat_session_id === sessionId && item.secret_names?.includes(name));
    expect(grant).toBeTruthy();
    await page.getByRole("button", { name: "Settings", exact: true }).click({ timeout: 15_000 });
    await expect(page.getByTestId("settings-view")).toBeVisible();
    await page.getByTestId("settings-nav-approvals").click();
    await page.getByTestId("approvals-tab-saved").click();
    await fillSolidControl(page.getByTestId("saved-approvals-search"), name);
    const row = page.locator(`[data-testid="saved-approvals-row"][data-grant-id="${grant!.id}"]`);
    await expect(row).toContainText(service.origin);
    const revoked = page.waitForResponse((response) => response.url().endsWith("/v1/approval-grants/revoke"));
    await row.getByTestId("saved-approvals-revoke").click();
    expect((await revoked).status()).toBe(200);
    await expect(row).toHaveCount(0);
    await page.getByTestId("settings-stage-close").click();
    expect(await settledChatSessionId(page)).toBe(sessionId);

    const next = await driver.invoke("http_request", httpArgs);
    await expect(cards()).toHaveCount(1, { timeout: 30_000 });
    await expect(cards().getByTestId("approval-secret-names")).toContainText(name);
    await expect(cards().getByTestId("approval-approve-primary")).toContainText("Send for this chat");
    expect(service.authenticatedRequests()).toBe(3);
    await expect(page.getByTestId("shell-workspace-veil")).toHaveAttribute("aria-hidden", "true", { timeout: 30_000 });
    await cards().screenshot({ path: "/tmp/secret-service-live-revoked.png", animations: "disabled" });
    await cards().getByTestId("approval-approve-primary").click();
    expect(await driver.completed(next)).toContain("authenticated");
    expect(service.authenticatedRequests()).toBe(4);
    await expect(page.locator("body")).not.toContainText(service.password());
    const usesResponse = await request.get(`${apiUrl}/v1/projects/${project.id}/secrets/${secretId}/uses`, { headers });
    expect(usesResponse.ok(), await usesResponse.text()).toBeTruthy();
    const uses = await usesResponse.json() as ManagedSecretUseList;
    expect(uses.uses.filter((use) => use.delivery === "handed_off")).toHaveLength(5);
  } catch (error) {
    await test.info().attach("service-browser-errors", {
      body: browserErrors.join("\n"), contentType: "text/plain",
    });
    if (sessionId) {
      const messages = await request.get(`${apiUrl}/v1/sessions/${sessionId}/messages`, { headers }).catch(() => undefined);
      if (messages) {
        await test.info().attach("service-tool-results", { body: await messages.body(), contentType: "application/json" });
      }
    }
    if (browserErrors.length) {
      const detail = error instanceof Error ? error.stack : String(error);
      throw new Error([...browserErrors.slice(0, 8), detail].join("\n"));
    }
    throw error;
  } finally {
    try {
      // A test timeout can close the request context before cleanup.
      await request.post(`${apiUrl}/harness/llm/auto`, { headers, data: { enabled: true } }).catch(() => undefined);
    } finally {
      await service.close();
    }
  }
});
