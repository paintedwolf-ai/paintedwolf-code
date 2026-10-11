// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { connectionServicesFixture } from "../../test/connection-services-fixture.ts";
import { noteBackendReachable, noteBackendUnreachable } from "./request-connectivity.ts";
import {
  installConnectionFixtureCleanup, loadModule, createLycaonClient, discoverBackend, emitEngineState, readSidecarInfo,
} from "./app-connection-test-fixture.ts";

installConnectionFixtureCleanup();

describe("connection service lifetime", () => {
  it("cannot retire a replacement binding through an older cleanup", async () => {
    const mod = await loadModule();
    for (let cycle = 0; cycle < 20; cycle++) {
      const first = connectionServicesFixture();
      const stopFirst = mod.bindAppConnection(first).dispose;
      noteBackendReachable();
      expect(first.app.state.sidecarStatus).toBe("connected");
      const second = connectionServicesFixture();
      const stopSecond = mod.bindAppConnection(second).dispose;
      stopFirst();
      expect(mod.getCostStore()).toBe(second.costs);
      expect(mod.getRegisteredNoticeStore()).toBe(second.notices);
      noteBackendReachable();
      expect(second.app.state.sidecarStatus).toBe("connected");
      expect(first.app.state.sidecarStatus).toBe("disconnected");
      stopSecond();
      noteBackendReachable();
      expect(second.app.state.sidecarStatus).toBe("disconnected");
      expect(mod.getCostStore()).toBeNull();
      expect(mod.getRegisteredNoticeStore()).toBeNull();
      noteBackendUnreachable();
    }
  });

  it("rejects discovery completing after the service binding was replaced", async () => {
    const mod = await loadModule();
    const first = connectionServicesFixture();
    const stopFirst = mod.bindAppConnection(first).dispose;
    let resolve!: (connection: { baseUrl: string; apiToken: string }) => void;
    discoverBackend.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    const pending = mod.connectAppBackend(first.app);
    const rejected = expect(pending).rejects.toThrow();
    await vi.waitFor(() => expect(discoverBackend).toHaveBeenCalledOnce());
    const second = connectionServicesFixture();
    const stopSecond = mod.bindAppConnection(second).dispose;
    resolve({ baseUrl: "http://old", apiToken: "old" });
    await rejected;
    expect(mod.getLycaonClient()).toBeNull();
    expect(second.app.state.sidecarStatus).not.toBe("connected");
    stopFirst();
    stopSecond();
  });

  it("closes engine subscriptions whose setup finishes after disposal", async () => {
    const mod = await loadModule();
    const services = connectionServicesFixture();
    const stop = mod.bindAppConnection(services).dispose;
    let resolve!: (stop: () => void) => void;
    const { watchEngineState } = await import("./engine-supervision.ts");
    vi.mocked(watchEngineState).mockReturnValueOnce(new Promise(done => { resolve = done; }));
    const following = mod.followEngineState(services.app);
    stop();
    const unsubscribe = vi.fn();
    resolve(unsubscribe);
    const stopFollowing = await following;
    expect(unsubscribe).toHaveBeenCalledOnce();
    stopFollowing();
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it("does not rebind from engine information received after disposal", async () => {
    const mod = await loadModule();
    const services = connectionServicesFixture();
    const stop = mod.bindAppConnection(services).dispose;
    await mod.followEngineState(services.app);
    emitEngineState({ state: "stopped", exit: { signal: 9, description: "stopped" } });
    let resolve!: (connection: { baseUrl: string; apiToken: string; engineGeneration: number }) => void;
    readSidecarInfo.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    emitEngineState({ state: "running", generation: 2 });
    stop();
    resolve({ baseUrl: "http://old", apiToken: "old", engineGeneration: 2 });
    await Promise.resolve();
    await Promise.resolve();
    expect(mod.getLycaonClient()).toBeNull();
    expect(discoverBackend).not.toHaveBeenCalled();
  });

  it("refuses old stores before joining a replacement flight or replacing its follower", async () => {
    const mod = await loadModule();
    const first = connectionServicesFixture();
    const firstBinding = mod.bindAppConnection(first);
    const second = connectionServicesFixture();
    const secondBinding = mod.bindAppConnection(second);
    await mod.followEngineState(second.app);
    let resolve!: (connection: { baseUrl: string; apiToken: string }) => void;
    discoverBackend.mockReturnValueOnce(new Promise(done => { resolve = done; }));
    const current = mod.connectAppBackend(second.app);
    await vi.waitFor(() => expect(discoverBackend).toHaveBeenCalledOnce());
    await expect(mod.connectAppBackend(first.app)).rejects.toThrow();
    await expect(mod.followEngineState(first.app)).rejects.toThrow();
    expect(() => mod.subscribeProjectEvents(first.app, "old-project")).toThrow();
    expect(() => mod.attachKnownBackend(first.app, { baseUrl: "http://old", apiToken: "old" })).toThrow();
    emitEngineState({ state: "restarting", exit: { signal: 9, description: "stopped" }, attempt: 1 });
    expect(second.app.state.sidecarStatus).toBe("disconnected");
    resolve({ baseUrl: "http://current", apiToken: "current" });
    expect(await current).toBe(mod.getLycaonClient());
    expect(firstBinding.signal.aborted).toBe(true);
    firstBinding.dispose();
    secondBinding.dispose();
  });

  it("shares cached backend verification across concurrent callers", async () => {
    const mod = await loadModule();
    const services = connectionServicesFixture();
    const stop = mod.bindAppConnection(services).dispose;
    const listProjects = vi.fn(async () => []);
    createLycaonClient.mockReturnValue({ listProjects });
    try {
      const client = await mod.connectAppBackend(services.app);
      const initial = listProjects.mock.calls.length;
      let resolve!: (rows: never[]) => void;
      listProjects.mockImplementationOnce(() => new Promise(done => { resolve = done; }));
      const first = mod.connectAppBackend(services.app);
      const second = mod.connectAppBackend(services.app);
      await vi.waitFor(() => expect(listProjects).toHaveBeenCalledTimes(initial + 1));
      resolve([]);
      expect(await first).toBe(client);
      expect(await second).toBe(client);
      expect(listProjects).toHaveBeenCalledTimes(initial + 1);
    } finally { stop(); }
  });
});
