import { describe, expect, it, vi } from "vitest";
import { includedProvider } from "../../../test/local-generation-fixture.ts";
import { createLocalGenerationActions } from "./local-generation-actions.ts";

describe("local model recovery", () => {
  it("admits one recovery per provider and publishes its resulting status", async () => {
    let finish!: () => void;
    const pending = new Promise<void>(resolve => { finish = resolve; });
    const provider = includedProvider();
    const client = {
      refreshProviderModels: vi.fn().mockReturnValue(pending),
      repairLocalGeneration: vi.fn(),
      listProviders: vi.fn().mockResolvedValue([provider]),
    };
    const publish = vi.fn();
    const actions = createLocalGenerationActions(() => client, publish);
    const first = actions.recover(provider, "retry");
    expect(actions.busy(provider.id)).toBe(true);
    await actions.recover(provider, "repair");
    expect(client.repairLocalGeneration).not.toHaveBeenCalled();
    finish();
    await first;
    expect(client.refreshProviderModels).toHaveBeenCalledExactlyOnceWith(provider.id);
    expect(publish).toHaveBeenCalledWith([provider]);
    expect(actions.busy(provider.id)).toBe(false);
    expect(actions.error(provider.id)).toBeUndefined();
  });

  it("preserves failed repair details while refreshing authoritative status", async () => {
    const provider = includedProvider({ state: "repair_required", recovery_action: "repair" });
    const client = {
      refreshProviderModels: vi.fn(),
      repairLocalGeneration: vi.fn().mockResolvedValue({ ok: false, status: { ...provider.local_runtime, detail: "Invalid bundle signature" } }),
      listProviders: vi.fn().mockResolvedValue([provider]),
    };
    const publish = vi.fn();
    const actions = createLocalGenerationActions(() => client, publish);
    await actions.recover(provider, "repair");
    expect(actions.error(provider.id)).toBe("Invalid bundle signature");
    expect(publish).toHaveBeenCalledWith([provider]);
    expect(actions.busy(provider.id)).toBe(false);
  });

  it("keeps the operation error if refreshing status also fails and permits another attempt", async () => {
    const provider = includedProvider();
    const client = {
      refreshProviderModels: vi.fn().mockRejectedValueOnce(new Error("Connection lost")).mockResolvedValue(undefined),
      repairLocalGeneration: vi.fn(),
      listProviders: vi.fn().mockRejectedValueOnce(new Error("Still disconnected")).mockResolvedValue([provider]),
    };
    const publish = vi.fn();
    const actions = createLocalGenerationActions(() => client, publish);
    await actions.recover(provider, "retry");
    expect(actions.error(provider.id)).toBe("Connection lost");
    expect(actions.busy(provider.id)).toBe(false);
    expect(publish).not.toHaveBeenCalled();
    await actions.recover(provider, "retry");
    expect(actions.error(provider.id)).toBeUndefined();
    expect(publish).toHaveBeenCalledWith([provider]);
  });
});
