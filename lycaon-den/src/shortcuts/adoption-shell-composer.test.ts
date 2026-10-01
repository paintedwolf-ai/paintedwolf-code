// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { registerShellDismissCommand } from "../components/shell/shell-dismiss-command.ts";
import { registerEscapeLadderLayer, resetEscapeLadderForTests } from "../find/escape-ladder.ts";
import {
  dispatchKeyboardEvent,
  registerCommandHandler,
  resetDispatcherForTests,
  setComposerFocused,
  setDispatcherPlatformForTests,
} from "./dispatcher.ts";
import { enterWalk, isWalking, resetWalkForTests } from "../files/walk/walk-store.ts";
import { stubFilesClient } from "../test/source-client-fixture.ts";
import {
  seedStockFrame,
  stockId,
} from "../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";

beforeEach(() => {
  seedStockFrame();
});

afterEach(() => {
  resetEscapeLadderForTests();
  resetDispatcherForTests();
  setDispatcherPlatformForTests(null);
  resetContributionStoreForTest();
});

describe("overlay.dismiss stack order", () => {
  it("closes find before search before launcher before workers before settings", () => {
    setDispatcherPlatformForTests("macos");
    const order: string[] = [];
    let find = true;
    let search = true;
    let launcher = true;
    let workers = true;
    let settings = true;

    registerEscapeLadderLayer("findBar", {
      isOpen: () => find,
      dismiss: () => { find = false; order.push("find"); },
    });
    const detachDismiss = registerShellDismissCommand({
      recentSwitcher: () => undefined,
      peerViewSwitcher: () => undefined,
      helpOpen: () => false,
      setHelpOpen: vi.fn(),
      crossbarOpen: () => false,
      closeCrossbar: vi.fn(),
      searchOpen: () => search,
      closeSearch: () => { search = false; order.push("search"); },
      launcherOpen: () => launcher,
      setLauncherOpen: (value) => { launcher = value; order.push("launcher"); },
      dockTray: () => null,
      closeLayoutTray: vi.fn(),
      layoutTrigger: () => undefined,
      workersOpen: () => workers,
      closeWorkers: () => { workers = false; order.push("workers"); },
      isSettingsNav: () => settings,
      splitLive: () => false,
      closeSettingsNav: () => { settings = false; order.push("settings"); },
      activeProjectId: () => null,
    });

    for (let i = 0; i < 5; i++) {
      dispatchKeyboardEvent({
        key: "Escape",
        code: "Escape",
        metaKey: false,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
      });
    }

    expect(order).toEqual(["find", "search", "launcher", "workers", "settings"]);
    detachDismiss();
    search = true;
    dispatchKeyboardEvent({
      key: "Escape", code: "Escape", metaKey: false, ctrlKey: false,
      altKey: false, shiftKey: false,
    });
    expect(search).toBe(true);
  });
});

describe("the files floor of the dismissal chain", () => {
  const escape = () => dispatchKeyboardEvent({
    key: "Escape", code: "Escape", metaKey: false, ctrlKey: false, altKey: false, shiftKey: false,
  });
  const dismissDependencies = (activeProjectId: () => string | null) => ({
    recentSwitcher: () => undefined,
    peerViewSwitcher: () => undefined,
    helpOpen: () => false,
    setHelpOpen: vi.fn(),
    crossbarOpen: () => false,
    closeCrossbar: vi.fn(),
    searchOpen: () => false,
    closeSearch: vi.fn(),
    launcherOpen: () => false,
    setLauncherOpen: vi.fn(),
    dockTray: () => null,
    closeLayoutTray: vi.fn(),
    layoutTrigger: () => undefined,
    workersOpen: () => false,
    closeWorkers: vi.fn(),
    isSettingsNav: () => false,
    splitLive: () => false,
    closeSettingsNav: vi.fn(),
    activeProjectId,
  });

  afterEach(resetWalkForTests);

  it("closes an open walk before leaving the past version it shows", () => {
    setDispatcherPlatformForTests("macos");
    void enterWalk("p1", stubFilesClient({ listProjectSourceWalk: () => new Promise<never>(() => {}) }), "s1");
    expect(isWalking("p1")).toBe(true);
    const currentVersion = vi.fn();
    registerCommandHandler("files.currentVersion", currentVersion);
    const detach = registerShellDismissCommand(dismissDependencies(() => "p1"));

    escape();
    expect(isWalking("p1")).toBe(false);
    expect(currentVersion).not.toHaveBeenCalled();

    escape();
    expect(currentVersion).toHaveBeenCalledOnce();
    detach();
  });

  it("leaves another project's walk alone", () => {
    setDispatcherPlatformForTests("macos");
    void enterWalk("p2", stubFilesClient({ listProjectSourceWalk: () => new Promise<never>(() => {}) }), "s1");
    const detach = registerShellDismissCommand(dismissDependencies(() => "p1"));
    escape();
    expect(isWalking("p2")).toBe(true);
    detach();
  });
});

describe("composer commands via dispatcher", () => {
  it("Enter sends and Shift+Enter newlines when composer focused", () => {
    setDispatcherPlatformForTests("macos");
    setComposerFocused(true, {});
    const send = vi.fn();
    const newline = vi.fn();
    registerCommandHandler("composer.send", send);
    registerCommandHandler("composer.newline", newline);

    const ta = document.createElement("textarea");
    document.body.appendChild(ta);

    const sendResult = dispatchKeyboardEvent({
      key: "Enter",
      code: "Enter",
      metaKey: false,
      ctrlKey: false,
      altKey: false,
      shiftKey: false,
      target: ta,
    });
    expect(sendResult).toEqual({
      commandId: stockId("composer-send"),
      chord: "Enter",
      handled: true,
    });
    expect(send).toHaveBeenCalledOnce();
    expect(newline).not.toHaveBeenCalled();

    const nlResult = dispatchKeyboardEvent({
      key: "Enter",
      code: "Enter",
      metaKey: false,
      ctrlKey: false,
      altKey: false,
      shiftKey: true,
      target: ta,
    });
    expect(nlResult.commandId).toBe(stockId("composer-newline"));
    expect(newline).toHaveBeenCalledOnce();
    ta.remove();
  });
});
