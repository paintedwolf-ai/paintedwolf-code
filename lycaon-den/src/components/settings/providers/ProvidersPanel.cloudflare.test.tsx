import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { ProviderMeta } from "../../../api/types.ts";
import { createSettingsStore } from "../../../store/settings-store.ts";
import { ProvidersPanel } from "./ProvidersPanel.tsx";

const template = {
  kind: "cloudflare-workers-ai",
  label: "Cloudflare Workers AI",
  base_url: "https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1",
  requires_api_key: true,
};
const accountId = "0123456789abcdef0123456789abcdef";
const completeUrl = template.base_url.replace("YOUR_ACCOUNT_ID", accountId);

function setup(providers: ProviderMeta[] = []) {
  const client = {
    listProviders: vi.fn().mockResolvedValue(providers),
    listProviderKinds: vi.fn().mockResolvedValue([template]),
    createProvider: vi.fn().mockResolvedValue(undefined),
    updateProvider: vi.fn().mockResolvedValue(undefined),
    testProvider: vi.fn().mockResolvedValue({ ok: true, models: 27, latency_ms: 10 }),
  };
  const settingsStore = createSettingsStore({ providers });
  render(() => (
    <ProvidersPanel
      client={client as never}
      settingsStore={settingsStore}
      providers={providers}
      onRemoveProvider={() => undefined}
    />
  ));
  return client;
}

describe("Cloudflare provider setup", () => {
  it("keeps connection testing read-only and explains that model access is unverified", async () => {
    const provider: ProviderMeta = {
      ...template,
      id: "cloudflare-workers-ai-1",
      base_url: completeUrl,
      configured: true,
      ready_to_assign: false,
      credential_present: true,
      features: { tool_calls: true, thinking: false, prompt_cache: "none" },
      models: [],
    };
    const client = setup([provider]);
    fireEvent.click(screen.getByTestId(`providers-row-${provider.id}`));
    expect(screen.getByTestId(`provider-test-scope-${provider.id}`).textContent).toContain("without running a model");
    fireEvent.click(screen.getByTestId(`provider-test-${provider.id}`));
    await waitFor(() => expect(client.testProvider).toHaveBeenCalledWith(provider.id));
    expect(screen.getByTestId(`provider-test-result-${provider.id}`).textContent).toContain("model access not verified");
  });

  it("requires an account ID before adding and saves the assembled endpoint", async () => {
    const client = setup();
    fireEvent.click(screen.getByTestId("providers-add"));
    await waitFor(() => expect(screen.getByTestId("add-provider-kind-cloudflare-workers-ai")).toBeTruthy());
    fireEvent.click(screen.getByTestId("add-provider-kind-cloudflare-workers-ai"));
    const account = screen.getByTestId("add-provider-account-id") as HTMLInputElement;
    const endpoint = screen.getByTestId("add-provider-base-url") as HTMLInputElement;
    const add = screen.getByTestId("add-provider-btn") as HTMLButtonElement;
    expect(account.value).toBe("");
    expect(add.disabled).toBe(true);
    expect(endpoint.getAttribute("aria-invalid")).toBe("true");
    fireEvent.click(add);
    expect(client.createProvider).not.toHaveBeenCalled();
    fireEvent.input(account, { target: { value: accountId } });
    expect(endpoint.value).toBe(completeUrl);
    expect(add.disabled).toBe(false);
    fireEvent.click(add);
    await waitFor(() => expect(client.createProvider).toHaveBeenCalledWith(
      expect.objectContaining({ id: "cloudflare-workers-ai-1", base_url: completeUrl }),
    ));
  });

  it("repairs an existing incomplete provider without replacing its identity or key", async () => {
    const provider: ProviderMeta = {
      ...template,
      id: "cloudflare-workers-ai-1",
      configured: false,
      ready_to_assign: false,
      credential_present: true,
      credential_source: "stored",
      features: { tool_calls: true, thinking: false, prompt_cache: "none" },
      models: [],
    };
    const client = setup([provider]);
    const row = screen.getByTestId(`providers-row-${provider.id}`);
    expect(row.textContent).toContain("Needs setup");
    fireEvent.click(row);
    const account = screen.getByTestId(`provider-account-id-${provider.id}`) as HTMLInputElement;
    const endpoint = screen.getByTestId(`provider-endpoint-${provider.id}`) as HTMLInputElement;
    const save = screen.getByTestId(`provider-save-endpoint-${provider.id}`) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    expect(screen.getByTestId(`provider-key-status-${provider.id}`).textContent).toContain("Key saved");
    expect(screen.queryByTestId(`provider-discovery-failure-${provider.id}`)).toBeNull();
    fireEvent.input(account, { target: { value: accountId } });
    expect(endpoint.value).toBe(completeUrl);
    fireEvent.input(endpoint, { target: { value: completeUrl.replace(accountId, "second-account") } });
    expect(account.value).toBe("second-account");
    fireEvent.input(account, { target: { value: "" } });
    expect(save.disabled).toBe(true);
    fireEvent.input(account, { target: { value: accountId } });
    fireEvent.click(save);
    await waitFor(() => expect(client.updateProvider).toHaveBeenCalledWith(provider.id, { base_url: completeUrl }));
  });
});
