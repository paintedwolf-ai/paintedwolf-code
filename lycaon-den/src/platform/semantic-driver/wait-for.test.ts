// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { installLycaonDriver } from "./page-api";

afterEach(() => {
  vi.useRealTimers();
  document.body.replaceChildren();
});

function addVisible(id: string): HTMLElement {
  const el = document.createElement("div");
  el.id = id;
  Object.defineProperty(el, "offsetParent", { get: () => document.body });
  document.body.append(el);
  return el;
}

it("wait_for gone settles once its target leaves the page and not before", async () => {
  vi.useFakeTimers();
  const toast = addVisible("toast");
  const driver = installLycaonDriver();
  const waiting = driver.waitFor({ selector: "#toast", gone: true, timeout_ms: 5_000 });
  let settled = false;
  void waiting.then(() => {
    settled = true;
  });
  await vi.advanceTimersByTimeAsync(1_000);
  expect(settled).toBe(false);
  toast.remove();
  await vi.advanceTimersByTimeAsync(200);
  await expect(waiting).resolves.toMatchObject({ ok: true });
});

it("wait_for gone reports its deadline when the target stays", async () => {
  vi.useFakeTimers();
  addVisible("toast");
  const driver = installLycaonDriver();
  const waiting = driver.waitFor({ selector: "#toast", gone: true, timeout_ms: 1_000 });
  await vi.advanceTimersByTimeAsync(1_200);
  await expect(waiting).resolves.toMatchObject({ ok: false, deadline_ms: 1_000, deadline_expired: true });
});

it("wait_for settles once its target appears and not before", async () => {
  vi.useFakeTimers();
  const driver = installLycaonDriver();
  const waiting = driver.waitFor({ selector: "#toast", timeout_ms: 5_000 });
  let settled = false;
  void waiting.then(() => {
    settled = true;
  });
  await vi.advanceTimersByTimeAsync(1_000);
  expect(settled).toBe(false);
  addVisible("toast");
  await vi.advanceTimersByTimeAsync(200);
  await expect(waiting).resolves.toMatchObject({ ok: true });
});

it("wait_for gone on text settles while the text is absent", async () => {
  vi.useFakeTimers();
  const driver = installLycaonDriver();
  const waiting = driver.waitFor({ text: "Saved", gone: true, timeout_ms: 1_000 });
  await vi.advanceTimersByTimeAsync(0);
  await expect(waiting).resolves.toMatchObject({ ok: true });
});

it("wait_for without a selector or text is refused", async () => {
  const driver = installLycaonDriver();
  await expect(driver.waitFor({})).resolves.toMatchObject({ ok: false, error: "wait_for requires selector or text" });
});

it("select reports the deadline it ran out on when its target never appears", async () => {
  vi.useFakeTimers();
  const driver = installLycaonDriver();
  const choosing = driver.select({ selector: "#plan", value: "pro", timeout_ms: 1_000 });
  await vi.advanceTimersByTimeAsync(1_200);
  await expect(choosing).resolves.toMatchObject({
    ok: false,
    error: "select target not found",
    deadline_ms: 1_000,
    deadline_expired: true,
  });
});
