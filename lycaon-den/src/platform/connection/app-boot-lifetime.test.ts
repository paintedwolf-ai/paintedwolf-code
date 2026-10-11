// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { connectionServicesFixture } from "../../test/connection-services-fixture.ts";
import { installConnectionFixtureCleanup, loadModule, discoverBackend, subscribeEvents } from "./app-connection-test-fixture.ts";

vi.mock("../../store/app-state-snapshot.ts", async (original) => ({
  ...await original<typeof import("../../store/app-state-snapshot.ts")>(),
  loadSharedAppState: vi.fn(),
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
