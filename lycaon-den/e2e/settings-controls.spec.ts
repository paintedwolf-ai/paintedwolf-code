import { expect, type Page } from "@playwright/test";
import { apiConfig, bootstrapChatSession, expectShellReady, waitHarnessConnected, webE2e } from "./helpers.ts";
import { appStateStorageSliceKey } from "../shared/app-state-storage.ts";
import type { CreateProviderRequest, DetectionPackList, ProviderMeta, UpdateProviderRequest } from "../src/api/types.ts";

async function openSettings(page: Page, section = "general") {
  await waitHarnessConnected(page);
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page.getByTestId("settings-view")).toBeVisible();
  await page.getByTestId(`settings-nav-${section}`).click();
}

webE2e("settings controls retain display, editor, and notification preferences", async ({ page }) => {
  await page.goto("/");
  await expectShellReady(page);
  await openSettings(page);
  const groups = [
    { tab: "display", ids: ["display-diff-collapsed", "display-diff-word-wrap", "display-show-all-models", "display-first-time-tips"] },
    { tab: "editor", ids: ["editor-word-wrap", "editor-line-numbers", "editor-indent-guides", "editor-whitespace", "editor-scrollbar-ticks-symbols", "editor-scrollbar-ticks-changes", "editor-scrollbar-ticks-matches", "editor-scrollbar-ticks-cursor", "editor-scrollbar-ticks-agents", "editor-agent-activity-reads", "editor-agent-activity-changes", "editor-agent-activity-fileNames"] },
    { tab: "notifications", ids: ["notifications-finished", "notifications-needs-you", "notifications-needs-approval"] },
  ];
  const values = new Map<string, boolean>();
  for (const group of groups) {
    await page.getByTestId(`general-tab-${group.tab}`).click();
    for (const id of group.ids) {
      const control = page.getByTestId(id);
      const value = !(await control.isChecked());
      await control.setChecked(value);
      await expect(control).toBeChecked({ checked: value });
      values.set(id, value);
    }
  }
  await page.getByTestId("notifications-enabled").uncheck();
  await expect(page.getByTestId("notifications-finished")).toBeDisabled();
  await page.getByTestId("notifications-enabled").check();
  await expect(page.getByTestId("notifications-finished")).toBeEnabled();
  await page.getByTestId("general-tab-editor").click();
  await page.getByTestId("editor-font-size-inc").click();
  await page.getByTestId("editor-line-height-inc").click();
  await page.getByTestId("editor-indent-tabs").click();
  await page.getByTestId("editor-indent-width-8").click();
  await page.getByTestId("editor-version-comparison-before").click();
  const fontSize = (await page.getByTestId("editor-font-size").textContent()) ?? "";
  const lineHeight = (await page.getByTestId("editor-line-height").textContent()) ?? "";
  await page.getByTestId("general-tab-display").click();
  await page.getByRole("button", { name: "Code font", exact: true }).click();
  await page.getByRole("option", { name: "Custom…", exact: true }).click();
  await page.getByRole("textbox", { name: "Code font family name", exact: true }).pressSequentially("Menlo");
  await page.getByRole("textbox", { name: "Code font family name", exact: true }).press("Tab");
  await page.getByTestId("appearance-mode").click();
  await page.getByRole("option", { name: "Dark", exact: true }).click();
  await page.getByTestId("product-text-scale").click();
  await page.getByRole("option", { name: "115%", exact: true }).click();
  await page.getByTestId("appearance-mode").click();
  await expect(page.getByRole("option", { name: "Dark", exact: true })).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Escape");
  await page.reload();
  await expectShellReady(page);
  await openSettings(page);
  await expect(page.getByTestId("appearance-mode")).toHaveText("Dark");
  await expect(page.getByTestId("product-text-scale")).toHaveText("115%");
  await expect(page.getByRole("textbox", { name: "Code font family name", exact: true })).toHaveValue("Menlo");
  for (const group of groups) {
    await page.getByTestId(`general-tab-${group.tab}`).click();
    for (const id of group.ids) await expect(page.getByTestId(id)).toBeChecked({ checked: values.get(id) });
  }
  await page.getByTestId("general-tab-editor").click();
  await expect(page.getByTestId("editor-font-size")).toHaveText(fontSize);
  await expect(page.getByTestId("editor-line-height")).toHaveText(lineHeight);
  for (const id of ["editor-indent-tabs", "editor-indent-width-8", "editor-version-comparison-before"]) {
    await expect(page.getByTestId(id)).toHaveAttribute("aria-pressed", "true");
  }
});

webE2e("settings budgets keep keyboard focus and persist every numeric field", async ({ page, request }) => {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const endpoint = `${apiUrl}/v1/settings/limits`;
  const baselineResponse = await request.get(endpoint, { headers });
  expect(baselineResponse.ok()).toBeTruthy();
  const baseline = await baselineResponse.json();
  const { scope: _scope, merged_from: _merged, ...original } = baseline;
  await page.goto("/");
  await expectShellReady(page);
  await openSettings(page, "debug");
  try {
    await page.getByTestId("spend-ceiling-enabled").check();
    const fields = [
      ["Spend safety ceiling (USD)", "session_spend_ceiling_nano_usd", "12.5"],
      ["Warn at (% of ceiling)", "spend_warning_ratio", "75"],
      ["Max iterations per turn", "max_iterations", "512"],
      ["Overlay promote max iterations", "overlay_promote_max_iterations", "151"],
      ["Max tool result bytes", "max_tool_result_bytes", "524289"],
      ["Max coordinator loop cycles", "max_coordinator_loop_cycles", "257"],
      ["Default worker ceiling", "worker_tool_budget_default", "21"],
      ["Minimum the coordinator may request", "worker_tool_budget_min", "3"],
      ["Maximum the coordinator may request", "worker_tool_budget_max", "100"],
      ["LLM turn timeout", "llm_turn_timeout_ms", "14401"],
      ["Coordinator host turn timeout", "coordinator_host_turn_timeout_ms", "86401"],
      ["Coordinator max sleep", "coordinator_max_sleep_ms", "10801"],
      ["Await parent workers timeout", "await_parent_workers_timeout_ms", "3601"],
    ] as const;
    await expect(page.getByTestId("settings-view").getByRole("spinbutton")).toHaveCount(fields.length);
    for (const [label, key, value] of fields) {
      const input = page.getByRole("spinbutton", { name: label, exact: true });
      await input.fill("");
      await input.pressSequentially(value);
      await expect(input).toBeFocused();
      await expect(input).toHaveValue(value);
      await expect.poll(async () => {
        const response = await request.get(endpoint, { headers });
        return (await response.json())[key];
      }).toBe(
        key === "spend_warning_ratio"
          ? Number(value) / 100
          : key === "session_spend_ceiling_nano_usd"
            ? Math.round(Number(value) * 1_000_000_000)
            : key.endsWith("_ms")
              ? Math.round(Number(value) * 1000)
              : Number(value),
      );
      await expect(input).toBeFocused();
    }
    await page.reload();
    await expectShellReady(page);
    await openSettings(page, "debug");
    for (const [label, , value] of fields) {
      await expect(page.getByRole("spinbutton", { name: label, exact: true })).toHaveValue(value);
    }
  } finally {
    const response = await request.patch(endpoint, { headers, data: original });
    expect(response.ok()).toBeTruthy();
  }
});

webE2e("settings explain unavailable desktop features without native errors", async ({ page, request }) => {
  const { apiUrl, token } = apiConfig();
  const preflight = await request.get(`${apiUrl}/v1/preflight`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(preflight.ok()).toBeTruthy();
  expect((await preflight.json()).probes).toContainEqual({ id: "scanner_engine", status: "ok" });
  await page.goto("/");
  await expectShellReady(page);
  await openSettings(page);
  await page.getByTestId("general-tab-notifications").click();
  await page.getByTestId("notifications-send-test").click();
  await expect(page.getByTestId("notifications-shell-unavailable")).toBeVisible();
  await expect(page.getByTestId("notifications-approval-denied")).toHaveCount(0);
  await page.getByTestId("general-tab-updates").click();
  await expect(page.getByTestId("updates-unavailable")).toContainText("desktop app");
  await expect(page.getByTestId("updates-install-status")).toHaveCount(0);
  for (const [section, label] of [["general", "General"], ["debug", "Advanced"]]) {
    await page.getByTestId(`settings-nav-${section}`).click();
    const tabs = page.getByRole("tablist", { name: `${label} settings sections`, exact: true }).getByRole("tab");
    for (const tab of await tabs.all()) {
      await tab.click();
      const panel = page.getByRole("tabpanel", { name: await tab.innerText(), exact: true });
      await expect(panel).toBeVisible();
      await expect(panel).toHaveAttribute("id", (await tab.getAttribute("aria-controls")) ?? "");
      await expect(panel).toHaveAttribute("aria-labelledby", (await tab.getAttribute("id")) ?? "");
    }
  }
});

webE2e("project secrets save metadata, deadlines, and replacement values", async ({ page, request }) => {
  const project = await bootstrapChatSession(page, request);
  const { apiUrl, token } = apiConfig();
  const endpoint = `${apiUrl}/v1/projects/${project.id}/secrets`;
  const headers = { Authorization: `Bearer ${token}` };
  const readSecret = async () => {
    const response = await request.get(endpoint, { headers });
    expect(response.ok()).toBeTruthy();
    return (await response.json()).secrets[0];
  };
  await page.getByRole("button", { name: "Project configuration", exact: true }).click();
  await page.getByRole("tab", { name: "Secrets", exact: true }).click();
  await page.getByRole("button", { name: "Add secret", exact: true }).click();
  await page.getByTestId("managed-secret-add-name").fill("Settings fixture");
  await page.getByTestId("managed-secret-add-purpose").fill("Disposable settings test");
  await page.getByTestId("managed-secret-add-value").fill("disposable-fixture-value");
  await page.getByTestId("managed-secret-add-submit").click();
  await expect(page.getByRole("heading", { name: "Settings fixture", exact: true })).toBeVisible();
  const reference = (await readSecret()).reference;

  await page.getByRole("button", { name: "Edit details", exact: true }).click();
  await page.getByRole("textbox", { name: "Name", exact: true }).fill("Updated fixture");
  await page.getByRole("textbox", { name: "Purpose", exact: true }).fill("Updated purpose");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Updated fixture", exact: true })).toBeVisible();
  await expect.poll(readSecret).toMatchObject({ name: "Updated fixture", purpose: "Updated purpose", reference });

  await page.getByRole("button", { name: "Set agent-use deadline", exact: true }).click();
  await page.getByRole("button", { name: "Agent-use deadline", exact: true }).click();
  await page.getByRole("option", { name: "In 30 days", exact: true }).click();
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect.poll(async () => (await readSecret()).agent_use_ends_at).toBeTruthy();

  await page.getByRole("button", { name: "Replace stored value", exact: true }).click();
  await page.getByRole("textbox", { name: "Paste the credential", exact: true }).fill("replacement-fixture-value");
  await page.getByRole("button", { name: "Replace stored value", exact: true }).click();
  await expect.poll(readSecret).toMatchObject({ version: 2, reference });
  await expect(page.getByRole("button", { name: "Edit details", exact: true })).toBeVisible();
  expect(await readSecret()).not.toHaveProperty("secret_value");
});

webE2e("editor settings persist custom fonts, external commands, and governed ticks", async ({ page }) => {
  await page.goto("/");
  await expectShellReady(page);
  await openSettings(page);
  await page.getByTestId("general-tab-editor").click();
  await page.getByTestId("editor-scrollbar-ticks").uncheck();
  for (const kind of ["symbols", "changes", "matches", "cursor"]) {
    await expect(page.getByTestId(`editor-scrollbar-ticks-${kind}`)).toBeDisabled();
  }
  await page.getByTestId("editor-scrollbar-ticks").check();
  await expect(page.getByTestId("editor-scrollbar-ticks-symbols")).toBeEnabled();
  await page.getByTestId("editor-scrollbar-ticks").uncheck();

  await page.getByTestId("editor-font-family").click();
  await page.getByRole("option", { name: "Custom…", exact: true }).click();
  await page.getByTestId("editor-font-family-custom").fill("Menlo");
  await page.getByTestId("editor-font-family-custom").press("Tab");
  await page.getByTestId("editor-external-editor").click();
  await page.getByRole("option", { name: "Custom", exact: true }).click();
  const editorCommand = "fixture-editor --line {line} {path}";
  const browserCommand = "fixture-browser {url}";
  await page.getByTestId("editor-custom-editor").fill(editorCommand);
  await page.getByTestId("editor-custom-editor").press("Tab");
  await page.getByTestId("editor-custom-folder").fill("fixture-editor {path}");
  await page.getByTestId("editor-custom-folder").press("Tab");
  for (const name of ["Chrome", "Firefox", "System default", "Custom"]) {
    await page.getByTestId("editor-browser").click();
    await page.getByRole("option", { name, exact: true }).click();
    await expect(page.getByTestId("editor-browser")).toHaveText(name);
  }
  await page.getByTestId("editor-custom-browser").fill(browserCommand);
  await page.getByTestId("editor-custom-browser").press("Tab");
  await page.reload();
  await expectShellReady(page);
  await openSettings(page);
  await page.getByTestId("general-tab-editor").click();
  await expect(page.getByTestId("editor-scrollbar-ticks")).not.toBeChecked();
  await expect(page.getByTestId("editor-scrollbar-ticks-symbols")).toBeDisabled();
  await expect(page.getByTestId("editor-font-family-custom")).toHaveValue("Menlo");
  await expect(page.getByTestId("editor-external-editor")).toHaveText("Custom");
  await expect(page.getByTestId("editor-custom-editor")).toHaveValue(editorCommand);
  await expect(page.getByTestId("editor-custom-folder")).toHaveValue("fixture-editor {path}");
  await expect(page.getByTestId("editor-browser")).toHaveText("Custom");
  await expect(page.getByTestId("editor-custom-browser")).toHaveValue(browserCommand);
  await expect(page.getByTestId("editor-custom-editor")).toBeVisible();
});

webE2e("web provider membership follows the catalog's individual and bundled controls", async ({ request }, testInfo) => {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const endpoint = `${apiUrl}/v1/web-research/providers`;
  const settingsEndpoint = `${apiUrl}/v1/settings/web-research`;
  const read = async () => {
    const response = await request.get(endpoint, { headers });
    expect(response.ok()).toBeTruthy();
    return response.json();
  };
  const original = await read();
  const ids: string[] = ["direct", ...original.providers.map((provider: { id: string }) => provider.id)];
  const bundled = new Set(original.providers.filter((provider: { kind: string; default_enabled?: boolean }) =>
    provider.kind === "keyless" && provider.default_enabled,
  ).map((provider: { id: string }) => provider.id));
  expect(new Set(ids).size).toBe(ids.length);
  expect(ids.length).toBeGreaterThan(1);
  try {
    for (const id of ids) {
      for (const enabled of [[id], []]) {
        const response = await request.patch(settingsEndpoint, { headers, data: { enabled_providers: enabled } });
        expect(response.ok(), `${id}: ${await response.text()}`).toBeTruthy();
        expect((await read()).enabled_providers, id).toEqual(bundled.has(id) ? [] : enabled);
      }
    }
    for (const [id, config] of [
      ["google_cse", { search_engine_id: "disposable-search-engine-id" }],
      ["searxng", { endpoint: "http://192.168.240.2:18080" }],
    ] as const) {
      const baseline = original.providers.find((provider: { id: string }) => provider.id === id)?.config ?? {};
      try {
        const response = await request.patch(`${endpoint}/${id}`, { headers, data: { config } });
        expect(response.ok(), `${id}: ${await response.text()}`).toBeTruthy();
        expect((await read()).providers.find((provider: { id: string }) => provider.id === id)?.config).toMatchObject(config);
      } finally {
        const response = await request.patch(`${endpoint}/${id}`, { headers, data: { config: baseline } });
        expect(response.ok()).toBeTruthy();
      }
    }
  } finally {
    const response = await request.patch(settingsEndpoint, { headers, data: { enabled_providers: original.enabled_providers } });
    expect(response.ok()).toBeTruthy();
  }
  await testInfo.attach("settings-web-provider-coverage", {
    body: JSON.stringify({ ids, bundled: [...bundled] }, null, 2),
    contentType: "application/json",
  });
});

webE2e("every settings shortcut can be rebound and reset without losing its identity", async ({ page, request }, testInfo) => {
  webE2e.setTimeout(300_000);
  const { apiUrl, token } = apiConfig();
  const response = await request.get(`${apiUrl}/v1/contributions`, { headers: { Authorization: `Bearer ${token}` } });
  expect(response.ok()).toBeTruthy();
  const frame = await response.json() as { keybindings: { id: string }[] };
  const expectedIds = ["nav.leader", ...frame.keybindings.map((binding) => binding.id)].sort();
  await page.goto("/");
  await expectShellReady(page);
  await openSettings(page);
  await page.getByTestId("general-tab-keyboard").click();
  const stored = (id: string) => page.evaluate(({ key, id }) => {
    const value = JSON.parse(localStorage.getItem(key) ?? "{}") as { overrides?: Record<string, string> };
    return value.overrides?.[id];
  }, { key: appStateStorageSliceKey("shortcuts"), id });
  const checked: { scope: string; id: string }[] = [];
  for (const scope of ["global", "files", "composer", "overlay"]) {
    await page.getByTestId(`keyboard-tab-${scope}`).click();
    const controls = page.locator('[data-testid^="keyboard-record-"]');
    const ids = await controls.evaluateAll((elements) => elements.map((element) =>
      element.getAttribute("data-testid")?.replace("keyboard-record-", "") ?? "",
    ));
    for (const id of ids) {
      expect(id).not.toBe("");
      const record = page.getByTestId(`keyboard-record-${id}`);
      await record.click();
      await expect(record).toHaveAttribute("aria-pressed", "true");
      await page.keyboard.press("ControlOrMeta+Shift+F8");
      await expect.poll(() => stored(id), id).toContain("F8");
      await page.getByTestId(`keyboard-reset-${id}`).click();
      await expect.poll(() => stored(id), id).toBeUndefined();
      await expect(page.getByTestId(`keyboard-reset-${id}`)).toBeDisabled();
      checked.push({ scope, id });
    }
  }
  expect(checked.map((row) => row.id).sort()).toEqual(expectedIds);
  await page.getByTestId("keyboard-tab-global").click();
  await page.getByTestId("keyboard-record-nav.leader").click();
  await page.keyboard.press("ControlOrMeta+Shift+F8");
  await expect.poll(() => stored("nav.leader")).toContain("F8");
  await page.reload();
  await expectShellReady(page);
  await openSettings(page);
  await page.getByTestId("general-tab-keyboard").click();
  await expect(page.getByTestId("keyboard-chord-nav.leader")).toHaveAttribute("data-binding", /F8/);
  await page.getByTestId("keyboard-reset-all").click();
  await expect.poll(() => stored("nav.leader")).toBeUndefined();
  await expect(page.getByTestId("keyboard-reset-all")).toHaveCount(0);
  await testInfo.attach("settings-shortcut-coverage", { body: JSON.stringify(checked, null, 2), contentType: "application/json" });
});

webE2e("never-ask settings require acknowledgement and can restore approvals", async ({ page, request }) => {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const endpoint = `${apiUrl}/v1/settings/approvals`;
  const read = async () => {
    const response = await request.get(endpoint, { headers });
    expect(response.ok()).toBeTruthy();
    return response.json();
  };
  const original = await read();
  const reset = await request.patch(endpoint, { headers, data: { never_ask: false } });
  expect(reset.ok()).toBeTruthy();
  try {
    await page.goto("/");
    await expectShellReady(page);
    await openSettings(page, "debug");
    await page.getByTestId("advanced-tab-approvals").click();
    await page.getByTestId("approvals-off-start").click();
    await expect(page.getByTestId("approvals-off-confirm-button")).toBeDisabled();
    await page.getByTestId("approvals-off-cancel").click();
    expect((await read()).never_ask ?? false).toBe(false);
    await page.getByTestId("approvals-off-start").click();
    await page.getByTestId("approvals-off-acknowledge").check();
    await page.getByTestId("approvals-off-confirm-button").click();
    await expect.poll(async () => (await read()).never_ask).toBe(true);
    await page.getByTestId("approvals-off-restore").click();
    await expect.poll(async () => (await read()).never_ask ?? false).toBe(false);
    await expect(page.getByTestId("approvals-off-start")).toBeVisible();
  } finally {
    const response = await request.patch(endpoint, { headers, data: { never_ask: original.never_ask ?? false } });
    expect(response.ok()).toBeTruthy();
  }
});

webE2e("provider deployment and credential-mode settings survive partial updates", async ({ request }) => {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const ids = ["settings-azure-fixture", "settings-bedrock-fixture"] as const;
  const create = async (data: CreateProviderRequest) => {
    const response = await request.post(`${apiUrl}/v1/providers`, { headers, data });
    expect(response.ok(), await response.text()).toBeTruthy();
  };
  const update = async (id: string, data: UpdateProviderRequest): Promise<ProviderMeta> => {
    const response = await request.patch(`${apiUrl}/v1/providers/${id}`, { headers, data });
    expect(response.ok(), await response.text()).toBeTruthy();
    return response.json();
  };
  try {
    const models = [{ id: "fixture-deployment", priced_as: "fixture-model" }];
    await create({ id: ids[0], kind: "azure", label: "Deployment fixture", base_url: "https://fixture.openai.azure.com", models });
    const azure = await update(ids[0], { label: "Renamed deployment fixture" });
    expect(azure.configured_models).toMatchObject(models);
    await create({ id: ids[1], kind: "bedrock", label: "Credential mode fixture", base_url: "us-east-1", requires_api_key: true });
    for (const required of [false, true]) {
      await update(ids[1], { requires_api_key: required });
      const provider = await update(ids[1], { label: "Retained credential mode" });
      expect(provider.requires_api_key).toBe(required);
      expect(provider.ambient_auth).toBe("aws-sdk-chain");
    }
  } finally {
    for (const id of ids) {
      const response = await request.delete(`${apiUrl}/v1/providers/${id}`, { headers });
      expect(response.ok() || response.status() === 404).toBeTruthy();
    }
  }
});

webE2e("every listed detection pack retains its enable setting", async ({ request }, testInfo) => {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const endpoint = `${apiUrl}/v1/detection-packs`;
  const read = async (): Promise<DetectionPackList> => {
    const response = await request.get(endpoint, { headers });
    expect(response.ok()).toBeTruthy();
    return response.json();
  };
  const original = await read();
  expect(original.packs.length).toBeGreaterThan(0);
  const checked: string[] = [];
  for (const pack of original.packs) {
    try {
      for (const enabled of [false, true]) {
        const response = await request.patch(`${endpoint}/${pack.id}`, { headers, data: { enabled } });
        expect(response.ok(), `${pack.id}: ${await response.text()}`).toBeTruthy();
        expect((await read()).packs.find((row) => row.id === pack.id)?.enabled, pack.id).toBe(enabled);
      }
      checked.push(pack.id);
    } finally {
      const response = await request.patch(`${endpoint}/${pack.id}`, { headers, data: { enabled: pack.enabled } });
      expect(response.ok()).toBeTruthy();
    }
  }
  await testInfo.attach("settings-detection-coverage", { body: JSON.stringify(checked, null, 2), contentType: "application/json" });
});
