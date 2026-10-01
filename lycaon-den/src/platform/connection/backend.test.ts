import { afterEach, describe, expect, it, vi } from "vitest";

const invokeMock = vi.fn();

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...args),
}));

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => true,
  waitForTauriRuntime: async () => true,
}));

vi.mock("../windows/window-channel.ts", () => ({
  listenHostEvent: async () => () => {},
}));

describe("discoverBackend", () => {
  afterEach(() => {
    vi.resetModules();
    invokeMock.mockReset();
  });

  it("maps start_sidecar invoke to baseUrl and apiToken", async () => {
    invokeMock.mockResolvedValueOnce({ port: 9123, api_token: "tok-abc" });
    const { discoverBackend } = await import("./backend.ts");
    const conn = await discoverBackend();
    expect(invokeMock).toHaveBeenCalledWith("start_sidecar", { password: null });
    expect(conn).toEqual({
      baseUrl: "http://127.0.0.1:9123",
      apiToken: "tok-abc",
    });
  });

  it("falls back to attach_existing_daemon when start_sidecar fails", async () => {
    invokeMock
      .mockRejectedValueOnce(new Error("spawn failed"))
      .mockResolvedValueOnce({ port: 8787, api_token: "attach-tok" });
    const { discoverBackend } = await import("./backend.ts");
    const conn = await discoverBackend();
    expect(invokeMock).toHaveBeenNthCalledWith(1, "start_sidecar", { password: null });
    expect(invokeMock).toHaveBeenNthCalledWith(2, "attach_existing_daemon");
    expect(conn.apiToken).toBe("attach-tok");
  });

  it("sends a supplied vault password to one managed start only", async () => {
    invokeMock.mockResolvedValueOnce({ port: 9123, api_token: "tok-abc" });
    const { discoverBackend, provideCredentialVaultPassword } = await import(
      "./backend.ts"
    );
    provideCredentialVaultPassword("correct horse battery staple");
    await discoverBackend();
    expect(invokeMock).toHaveBeenCalledWith("start_sidecar", {
      password: "correct horse battery staple",
    });
  });

  it("falls back to VITE dev token when tauri invoke paths fail", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: async () => ({
          status: "ok",
          version: "0.1.0",
          store_revision: 0,
          schema_version: 1,
        }),
      }),
    );
    invokeMock
      .mockRejectedValueOnce(new Error("spawn failed"))
      .mockResolvedValueOnce(null);
    vi.stubEnv("DEV", true);
    vi.stubEnv("VITE_LYCAON_API_TOKEN", "dev-token");
    vi.stubEnv("VITE_LYCAON_API_URL", "http://127.0.0.1:8787");
    const { discoverBackend } = await import("./backend.ts");
    const conn = await discoverBackend();
    expect(conn).toEqual({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "dev-token",
    });
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });
});

describe("restartBackend", () => {
  afterEach(() => {
    vi.resetModules();
    invokeMock.mockReset();
  });

  it("clears cache and invokes restart_sidecar", async () => {
    invokeMock.mockResolvedValueOnce({ port: 4000, api_token: "new-tok" });
    const { restartBackend, getBackendConnection } = await import(
      "./backend.ts"
    );
    const conn = await restartBackend();
    expect(invokeMock).toHaveBeenCalledWith("restart_sidecar");
    expect(conn.apiToken).toBe("new-tok");
    expect(getBackendConnection()?.apiToken).toBe("new-tok");
  });
});

describe("cancelBackendStart", () => {
  afterEach(() => {
    vi.resetModules();
    invokeMock.mockReset();
  });

  it("requests cancellation through the managed lifecycle command", async () => {
    invokeMock.mockResolvedValueOnce(true);
    const { cancelBackendStart } = await import("./backend.ts");
    await expect(cancelBackendStart()).resolves.toBe(true);
    expect(invokeMock).toHaveBeenCalledWith("cancel_sidecar_start");
  });
});

describe("resetCredentialVault", () => {
  afterEach(() => {
    vi.resetModules();
    invokeMock.mockReset();
  });

  it("invokes the narrow desktop reset command", async () => {
    invokeMock.mockResolvedValueOnce(undefined);
    const { resetCredentialVault } = await import("./backend.ts");
    await resetCredentialVault();
    expect(invokeMock).toHaveBeenCalledWith("reset_credential_vault");
  });
});
