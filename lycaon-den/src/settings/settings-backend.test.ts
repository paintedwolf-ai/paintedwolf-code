import { beforeEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../store/app-state.ts";

const { getLycaonClient } = vi.hoisted(() => ({
  getLycaonClient: vi.fn(),
}));

vi.mock("../platform/connection/app-connection.ts", () => ({
  getLycaonClient,
}));

import { useSettingsBackend } from "./settings-backend.ts";

describe("useSettingsBackend", () => {
  beforeEach(() => {
    getLycaonClient.mockReset();
  });

  it("keeps the REST client available while the event stream reconnects", () => {
    const client = { listProjects: vi.fn() };
    getLycaonClient.mockReturnValue(client);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const backend = useSettingsBackend(appStore);

    expect(backend.client()).toBe(client);
    appStore.actions.setSidecarStatus("reconnecting");

    expect(backend.client()).toBe(client);
    expect(backend.backendConnecting()).toBe(false);
  });

  it("shows connecting only when no usable client exists", () => {
    getLycaonClient.mockReturnValue(null);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connecting");
    const backend = useSettingsBackend(appStore);

    expect(backend.client()).toBeNull();
    expect(backend.backendConnecting()).toBe(true);
  });
});
