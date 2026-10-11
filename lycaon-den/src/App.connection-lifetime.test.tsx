import { render } from "@solidjs/testing-library";
import { expect, it, vi } from "vitest";
import App from "./App.tsx";
import { connectAppBackend, disconnectAppBackend, getRegisteredNoticeStore } from "./platform/connection/app-connection.ts";

const updates = vi.hoisted(() => ({ handlers: [] as Array<() => void>, resolve: [] as Array<(stop: () => void) => void> }));
vi.mock("./platform/runtime.ts", async (original) => ({
  ...await original<typeof import("./platform/runtime.ts")>(),
  isTauriRuntime: () => true,
  usesCustomWindowChrome: () => false,
  tauriDragRegionProps: () => ({}),
  tauriPlatform: () => null,
  detectedPlatform: () => null,
}));
vi.mock("./components/shell/Shell.tsx", () => ({ Shell: () => null }));
vi.mock("./platform/connection/app-boot.ts", () => ({ startAppBoot: async () => {} }));
vi.mock("./notifications/mount-notification-service.ts", () => ({ mountNotificationService: () => () => {} }));
vi.mock("./settings/system/mount-update-notice.ts", () => ({ mountUpdateNotice: () => () => {} }));
vi.mock("./platform/windows/window-channel.ts", async (original) => ({
  ...await original<typeof import("./platform/windows/window-channel.ts")>(),
  listenHostEvent: vi.fn((name: string, handler: () => void) => {
    if (name !== "update-resume-engine") return Promise.resolve(() => {});
    updates.handlers.push(handler);
    return new Promise<() => void>(resolve => updates.resolve.push(resolve));
  }),
}));
vi.mock("./platform/connection/app-connection.ts", async (original) => {
  const actual = await original<typeof import("./platform/connection/app-connection.ts")>();
  return { ...actual, disconnectAppBackend: vi.fn(actual.disconnectAppBackend), connectAppBackend: vi.fn().mockResolvedValue(null) };
});

it("retires update listeners and rejects late callbacks when another App binds", async () => {
  const first = render(() => <App />);
  const stopFirst = vi.fn();
  updates.resolve[0]!(stopFirst);
  await Promise.resolve();
  const second = render(() => <App />);
  expect(stopFirst).toHaveBeenCalledOnce();
  vi.mocked(disconnectAppBackend).mockClear();
  const notices = getRegisteredNoticeStore();
  updates.handlers[0]!();
  first.unmount();
  updates.handlers[0]!();
  expect(disconnectAppBackend).not.toHaveBeenCalled();
  expect(connectAppBackend).not.toHaveBeenCalled();
  expect(getRegisteredNoticeStore()).toBe(notices);
  updates.handlers[1]!();
  expect(disconnectAppBackend).toHaveBeenCalledOnce();
  expect(connectAppBackend).toHaveBeenCalledOnce();
  second.unmount();
  const stopSecond = vi.fn();
  updates.resolve[1]!(stopSecond);
  await Promise.resolve();
  expect(stopSecond).toHaveBeenCalledOnce();
  updates.handlers[1]!();
  expect(connectAppBackend).toHaveBeenCalledOnce();
  expect(getRegisteredNoticeStore()).toBeNull();
});
