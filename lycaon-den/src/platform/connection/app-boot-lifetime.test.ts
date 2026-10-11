// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { connectionServicesFixture } from "../../test/connection-services-fixture.ts";
import { installConnectionFixtureCleanup, loadModule, discoverBackend, subscribeEvents } from "./app-connection-test-fixture.ts";

vi.mock("../../store/app-state-snapshot.ts", async (original) => ({
  ...await original<typeof import("../../store/app-state-snapshot.ts")>(),
  loadSharedAppState: vi.fn().mockResolvedValue({ version: 1, recents: [] }),
}));
vi.mock("./boot-fallback.ts", () => ({ dismissBootFallback: vi.fn() }));
vi.mock("../../files/documents/files-hot-exit-window.ts", () => ({ watchWindowExitForHotExit: vi.fn() }));
vi.mock("../../files/tree/files-tree-view-window.ts", () => ({ watchWindowExitForFilesTreeView: vi.fn() }));
vi.mock("../persistence/app-state-window.ts", () => ({ watchWindowExitForAppState: vi.fn() }));

installConnectionFixtureCleanup();

it("cannot start connection work when the binding retires during boot state loading", async () => {
  const mod = await loadModule();
  const { loadSharedAppState } = await import("../../store/app-state-snapshot.ts");
  const { startAppBoot } = await import("./app-boot.ts");
  const { appBootPreparation } = await import("./app-boot-readiness.ts");
  const { watchEngineState } = await import("./engine-supervision.ts");
  vi.mocked(watchEngineState).mockClear();
  let resolve!: (state: never) => void;
  vi.mocked(loadSharedAppState).mockReturnValueOnce(new Promise(done => { resolve = done; }));
  const services = connectionServicesFixture();
  const binding = mod.bindAppConnection(services);
  const boot = startAppBoot({
    appStore: services.app, projects: services.projects, recents: services.recents, shell: services.shell,
  }, binding.signal);
  binding.dispose();
  expect(binding.signal.aborted).toBe(true);
  expect(appBootPreparation.ready()).toBe(true);
  resolve({ version: 1, recents: [] } as never);
  await boot;
  expect(discoverBackend).not.toHaveBeenCalled();
  expect(subscribeEvents).not.toHaveBeenCalled();
  expect(watchEngineState).not.toHaveBeenCalled();
  expect(mod.getLycaonClient()).toBeNull();
  expect(mod.getRegisteredNoticeStore()).toBeNull();
});

it("stops a late engine follower when boot is canceled during registration", async () => {
  const mod = await loadModule();
  const { startAppBoot } = await import("./app-boot.ts");
  const { watchEngineState } = await import("./engine-supervision.ts");
  const services = connectionServicesFixture();
  const binding = mod.bindAppConnection(services);
  const controller = new AbortController();
  let finish!: (stop: () => void) => void;
  vi.mocked(watchEngineState).mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  const boot = startAppBoot({ appStore: services.app, projects: services.projects, recents: services.recents, shell: services.shell }, controller.signal);
  await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
  controller.abort();
  const stop = vi.fn(); finish(stop);
  await boot;
  expect(stop).toHaveBeenCalledOnce();
  expect(discoverBackend).not.toHaveBeenCalled();
  binding.dispose();
});

it("cannot publish connection or session work after retirement during discovery", async () => {
  const mod = await loadModule();
  const { startAppBoot } = await import("./app-boot.ts");
  const { appBootPreparation } = await import("./app-boot-readiness.ts");
  const services = connectionServicesFixture();
  const binding = mod.bindAppConnection(services);
  let finish!: (connection: { baseUrl: string; apiToken: string }) => void;
  discoverBackend.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  await startAppBoot({ appStore: services.app, projects: services.projects, recents: services.recents, shell: services.shell }, binding.signal);
  await vi.waitFor(() => expect(discoverBackend).toHaveBeenCalledOnce());
  binding.dispose(); finish({ baseUrl: "http://test", apiToken: "test" });
  await vi.waitFor(() => expect(appBootPreparation.ready()).toBe(true));
  expect(subscribeEvents).not.toHaveBeenCalled();
  expect(services.shell.state.foreground).toBeNull();
  expect(mod.getLycaonClient()).toBeNull();
});


it("does not install boot resources for an already retired window", async () => {
  const { startAppBoot } = await import("./app-boot.ts");
  const { loadSharedAppState } = await import("../../store/app-state-snapshot.ts");
  const services = connectionServicesFixture(); const controller = new AbortController();
  controller.abort(); vi.mocked(loadSharedAppState).mockClear();
  await startAppBoot({ appStore: services.app, projects: services.projects, recents: services.recents, shell: services.shell }, controller.signal);
  expect(loadSharedAppState).not.toHaveBeenCalled();
  expect(discoverBackend).not.toHaveBeenCalled();
});

it("abandons restoration if retirement happens while recent sessions load", async () => {
  const mod = await loadModule();
  const { startAppBoot } = await import("./app-boot.ts");
  const services = connectionServicesFixture(); const binding = mod.bindAppConnection(services);
  let finish!: () => void;
  vi.spyOn(services.recents, "load").mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  const boot = startAppBoot({ appStore: services.app, projects: services.projects, recents: services.recents, shell: services.shell }, binding.signal);
  await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
  binding.dispose(); finish(); await boot;
  expect(discoverBackend).not.toHaveBeenCalled();
  expect(services.shell.state.foreground).toBeNull();
});
