import { installConnectionFixtureCleanup, discoverBackend, createLycaonClient, readSidecarInfo, loadModule, loadModuleWithNotices, resetEngineStateHandler, emitEngineState } from "./app-connection-test-fixture.ts";

import { beforeEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";

installConnectionFixtureCleanup();
  describe("engine supervision", () => {
    const exit = { signal: 9, description: "was killed by signal 9 (SIGKILL)" };

    beforeEach(() => {
      resetEngineStateHandler();
      readSidecarInfo.mockReset();
    });

    it("rebinds to the replacement engine and withdraws the restarting notice", async () => {
      const { mod, all } = await loadModuleWithNotices();
      const appStore = createAppStore();
      discoverBackend.mockResolvedValue({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok", engineGeneration: 1 });
      await mod.followEngineState(appStore);
      await mod.connectAppBackend(appStore);
      expect(mod.boundEngineGeneration()).toBe(1);

      emitEngineState({ state: "restarting", exit, attempt: 1 });
      expect(appStore.state.sidecarStatus).toBe("disconnected");
      expect(all().map((notice) => notice.code)).toContain("engine_restarting");

      const replacement = { baseUrl: "http://127.0.0.1:9911", apiToken: "replacement", engineGeneration: 2 };
      readSidecarInfo.mockResolvedValue(replacement);
      createLycaonClient.mockClear();
      emitEngineState({ state: "running", generation: 2 });
      await vi.waitFor(() => expect(mod.boundEngineGeneration()).toBe(2));

      expect(createLycaonClient).toHaveBeenCalledWith(replacement);
      expect(discoverBackend).toHaveBeenCalledTimes(1);
      expect(appStore.state.sidecarStatus).toBe("connected");
      expect(all().map((notice) => notice.code)).not.toContain("engine_restarting");
    });

    it("leaves a booting window to its own connect", async () => {
      const mod = await loadModule();
      await mod.followEngineState(createAppStore());
      emitEngineState({ state: "running", generation: 1 });
      await Promise.resolve();
      expect(readSidecarInfo).not.toHaveBeenCalled();
    });

    it("keeps the engine it is already bound to", async () => {
      const mod = await loadModule();
      const appStore = createAppStore();
      discoverBackend.mockResolvedValue({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok", engineGeneration: 3 });
      await mod.followEngineState(appStore);
      await mod.connectAppBackend(appStore);
      emitEngineState({ state: "running", generation: 3 });
      await Promise.resolve();
      expect(readSidecarInfo).not.toHaveBeenCalled();
    });

    it("rebinds a window that learns of an intentional restart elsewhere", async () => {
      const mod = await loadModule();
      const appStore = createAppStore();
      discoverBackend.mockResolvedValue({ baseUrl: "http://127.0.0.1:8787", apiToken: "tok", engineGeneration: 4 });
      await mod.followEngineState(appStore);
      await mod.connectAppBackend(appStore);
      readSidecarInfo.mockResolvedValue({ baseUrl: "http://127.0.0.1:9912", apiToken: "restored", engineGeneration: 5 });
      emitEngineState({ state: "idle" });
      emitEngineState({ state: "running", generation: 5 });
      await vi.waitFor(() => expect(mod.boundEngineGeneration()).toBe(5));
    });

    it("goes offline without a stop of its own when the shell gives up", async () => {
      const mod = await loadModule();
      const appStore = createAppStore();
      await mod.followEngineState(appStore);
      await mod.connectAppBackend(appStore);
      emitEngineState({ state: "stopped", exit });
      expect(appStore.state.sidecarStatus).toBe("disconnected");
      expect(readSidecarInfo).not.toHaveBeenCalled();
    });
  });
