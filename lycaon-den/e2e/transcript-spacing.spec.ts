import { expect, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { writeFileSync } from "node:fs";
import { setTranscriptSpacingForTest as setSpacing } from "./transcript-spacing-helpers.ts";
import { apiConfig, apiSeedSessionTranscript, bootstrapChatSession, liveChatStage, modelIndependentWebE2e as test, settledChatSessionId, transcriptMessages } from "./helpers.ts";

test.afterEach(async ({ page }, info) => {
  if (info.status === info.expectedStatus) return;
  const samples = await page.evaluate(() => {
    const trace = window as unknown as { __geometryTrace?: () => unknown; __geometryDetails?: () => unknown };
    trace.__geometryTrace?.();
    return trace.__geometryDetails?.();
  });
  if (samples) {
    const path = info.outputPath("transcript-geometry.json");
    writeFileSync(path, JSON.stringify(samples));
    await info.attach("transcript-geometry", { path, contentType: "application/json" });
  }
});

async function phase(page: Page, value: string) {
  await page.evaluate((value) => { (window as unknown as { __geometryPhase: string }).__geometryPhase = value; }, value);
}

async function settleLayout(page: Page) {
  await page.evaluate(async () => {
    for (let frame = 0; frame < 3; frame++) await new Promise(requestAnimationFrame);
  });
}

async function position(stream: Locator) {
  return stream.evaluate((host) => {
    const top = host.getBoundingClientRect().top;
    const row = [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")]
      .find((row) => row.getBoundingClientRect().bottom > top);
    return { key: row?.dataset.msgId, offset: row ? row.getBoundingClientRect().top - top : 0 };
  });
}
async function tail(stream: Locator) {
  return stream.evaluate((host) => {
    const end = host.querySelector<HTMLElement>("[data-transcript-end]")!;
    const body = host.querySelector<HTMLElement>(".den-chat-stream-body")!;
    return end.getBoundingClientRect().bottom + Number.parseFloat(getComputedStyle(body).paddingBottom) - host.getBoundingClientRect().bottom;
  });
}
async function trace(stream: Locator) {
  return stream.evaluate((host) => {
    const samples: { time: number; tail: number; offset: number; key?: string; phase?: string; scrollTop: number; scrollHeight: number; viewportHeight: number; row?: string }[] = [];
    let active = true;
    let settled: (typeof samples)[number] | undefined;
    const sample = () => {
      if (!active) return;
      const frame = host.getBoundingClientRect();
      const rows = [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")];
      const row = rows.find((row) => row.getBoundingClientRect().bottom > frame.top);
      const end = host.querySelector<HTMLElement>("[data-transcript-end]")!;
      const body = host.querySelector<HTMLElement>(".den-chat-stream-body")!;
      settled = { time: performance.now(), tail: end.getBoundingClientRect().bottom + Number.parseFloat(getComputedStyle(body).paddingBottom) - frame.bottom,
        offset: row ? row.getBoundingClientRect().top - frame.top : 0, key: row?.dataset.msgId,
        phase: (window as unknown as { __geometryPhase?: string }).__geometryPhase,
        scrollTop: host.scrollTop, scrollHeight: host.scrollHeight, viewportHeight: frame.height,
        row: row?.textContent?.slice(0, 80) };
    };
    // Sample layout deliveries after the application's pre-paint reconciliation.
    // A timer can force fresh layout before ResizeObserver has seen that layout.
    let pending = false;
    const observer = new ResizeObserver(() => {
      if (pending) return;
      pending = true;
      queueMicrotask(() => { pending = false; sample(); });
    });
    observer.observe(host);
    for (const child of host.querySelectorAll(".den-chat-stream-body, .den-chat-stream-inner")) {
      observer.observe(child, { box: "border-box" });
    }
    // A frame paints its last delivery: rows measured late in one delivery publish the
    // extent the application reconciles in a later delivery of the same frame.
    const publish = () => {
      if (settled) samples.push(settled);
      settled = undefined;
    };
    const frame = () => {
      publish();
      if (active) requestAnimationFrame(frame);
    };
    requestAnimationFrame(frame);
    (window as unknown as { __geometryDetails: () => unknown }).__geometryDetails = () => ({ samples });
    (window as unknown as { __geometryTrace: () => typeof samples }).__geometryTrace = () => {
      active = false;
      observer.disconnect();
      publish();
      return samples;
    };
  });
}
async function stopTrace(page: Page) {
  return page.evaluate(() => (window as unknown as { __geometryTrace: () => { tail: number; offset: number; key?: string }[] }).__geometryTrace());
}
async function seed(page: Page, request: APIRequestContext) {
  await bootstrapChatSession(page, request);
  const revision = await page.evaluate(async () => {
    const path = "/src/chat/transcript/layout/transcript-layout-revision.ts";
    const layout = await import(/* @vite-ignore */ path) as typeof import("../src/chat/transcript/layout/transcript-layout-revision.ts");
    return layout.transcriptLayoutRevision();
  });
  expect(revision).toMatch(/^[a-f0-9]{64}$/);
  const sessionId = await settledChatSessionId(page);
  await apiSeedSessionTranscript(request, sessionId, transcriptMessages(Array.from({ length: 180 }, (_, index) => ({
    id: crypto.randomUUID(), role: index % 2 ? "assistant" as const : "user" as const,
    content: `Spacing ${index}. ${"A stable paragraph of readable content. ".repeat(12)}\n\nAnother paragraph.`,
    created_at: "2026-09-15T12:00:00Z", seq: index + 1, ord: index + 1,
  }))));
  const stream = liveChatStage(page).locator(".den-chat-stream");
  await expect(stream.locator(".transcript-viewport-row").getByText("Spacing 179.", { exact: false })).toBeAttached();
  await stream.focus();
  await page.keyboard.press("End");
  await expect.poll(() => tail(stream).then(Math.abs)).toBeLessThan(2);
  return { stream, sessionId };
}
async function readHistory(page: Page, stream: Locator) {
  await stream.press("PageUp");
  await page.waitForTimeout(400);
  // Place the fixture beyond the jump button's near-tail threshold after native key motion.
  await stream.evaluate((host) => { host.scrollTop = host.scrollHeight - host.clientHeight * 3; });
  await settleLayout(page);
  await expect(page.getByTestId("stream-scroll-jump")).toHaveAttribute("aria-hidden", "false");
  // Pick a point near a row's beginning that remains inside it after contraction.
  await stream.evaluate((host) => {
    const top = host.getBoundingClientRect().top;
    const row = [...host.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-msg-id]")]
      .find((row) => row.getBoundingClientRect().top > top);
    if (!row) throw new Error("visible reading row is missing");
    host.scrollTop += row.getBoundingClientRect().top - top + 10;
  });
  await page.waitForTimeout(200);
}

for (const following of [true, false]) {
  test(`spacing changes preserve ${following ? "the live tail" : "the reading row"} across virtual windows`, async ({ page, request }) => {
    const { stream } = await seed(page, request);
    if (!following) { await trace(stream); await readHistory(page, stream); await stopTrace(page); }
    const reading = await position(stream);
    await trace(stream);
    for (const spacing of [
      { turnGap: 4.8125 },
      { sectionGap: 2.5 },
      { rowGap: 1.9375 },
      { paragraphGap: 1.2, userPaddingY: 1.2143 },
      { rowGap: 0, sectionGap: 0, turnGap: 0 },
      {},
    ]) {
      await phase(page, `spacing ${JSON.stringify(spacing)}`);
      await setSpacing(page, spacing);
      await settleLayout(page);
      await expect.poll(async () => {
        if (following) return Math.abs(await tail(stream)) < 2;
        const now = await position(stream);
        return now.key === reading.key && Math.abs(now.offset - reading.offset) < 2 ? true : JSON.stringify({ spacing, reading, now });
      }).toBe(true);
      const alignment = await stream.evaluate((host) => {
        const root = host.querySelector<HTMLElement>(".den-chat-stream-inner")!;
        const top = root.getBoundingClientRect().top;
        return [...root.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-index]")]
          .map((row) => Math.abs(row.getBoundingClientRect().top - top - Number(row.dataset.virtualStart)));
      });
      expect(Math.max(...alignment)).toBeLessThan(2);
    }
    for (const scale of [1.25, 1]) {
      await phase(page, `scale ${scale}`);
      await page.evaluate((scale) => {
        (window as unknown as { __harness: { setTextScale: (scale: number) => void } }).__harness.setTextScale(scale);
      }, scale);
      await settleLayout(page);
      await expect.poll(async () => {
        if (following) return Math.abs(await tail(stream)) < 2;
        const now = await position(stream);
        return now.key === reading.key && Math.abs(now.offset - reading.offset) < 2 ? true : JSON.stringify({ scale, reading, now });
      }).toBe(true);
    }
    const samples = await stopTrace(page);
    expect(samples.length).toBeGreaterThan(0);
    if (following) expect(Math.max(...samples.map((sample) => Math.abs(sample.tail)))).toBeLessThan(2);
    else {
      expect(samples.every((sample) => sample.key === reading.key)).toBe(true);
      expect(Math.max(...samples.map((sample) => Math.abs(sample.offset - reading.offset)))).toBeLessThan(2);
    }
  });

  test(`chat chrome preserves ${following ? "autofollow" : "reading position"} with expanded spacing`, async ({ page, request }) => {
    const { stream, sessionId } = await seed(page, request);
    await setSpacing(page, { rowGap: 1.6786, turnGap: 4.8125, paragraphGap: 1, userPaddingY: 0.9286 });
    await expect.poll(() => tail(stream).then(Math.abs)).toBeLessThan(2);
    if (!following) { await trace(stream); await readHistory(page, stream); await stopTrace(page); }
    const reading = await position(stream);
    const stage = liveChatStage(page);
    await trace(stream);
    const composer = stage.getByTestId("chat-composer");
    await phase(page, "composer grow");
    await composer.fill("A line\n".repeat(12));
    await page.waitForTimeout(400);
    await phase(page, "composer shrink");
    await composer.fill("");
    await page.waitForTimeout(400);
    await phase(page, "tab open");
    await stage.getByTestId("tab-progress").click();
    await page.waitForTimeout(400);
    await phase(page, "tab close");
    await stage.getByTestId("tab-progress").click();
    await page.waitForTimeout(400);
    await phase(page, "notification show");
    await page.evaluate(() => {
      const harness = (window as unknown as { __harness: { publishNotice: (notice: unknown) => unknown } }).__harness;
      harness.publishNotice({ code: "geometry_notice", title: "Layout notification", message: "Details that expand the notification. ".repeat(12) });
    });
    const notice = stage.getByTestId("notice").filter({ hasText: "Layout notification" });
    await expect(notice).toBeVisible();
    await phase(page, "notification collapse");
    await notice.getByRole("button", { name: "Hide details" }).click();
    await page.waitForTimeout(400);
    await phase(page, "notification expand");
    await notice.getByRole("button", { name: "Show details" }).click();
    await page.waitForTimeout(400);
    await phase(page, "notification dismiss");
    await notice.getByRole("button", { name: "Dismiss", exact: true }).click();
    const { apiUrl, token } = apiConfig();
    const headers = { Authorization: `Bearer ${token}` };
    await phase(page, "question show");
    const ask = await request.post(`${apiUrl}/harness/ask_user`, { headers, data: { session_id: sessionId, prompt: "Choose a direction. ".repeat(20) } });
    expect(ask.ok(), await ask.text()).toBe(true);
    const dock = stage.getByTestId("ask-user-dock");
    await expect(dock).toBeVisible();
    await page.waitForTimeout(400);
    await phase(page, "question minimize");
    await dock.getByTestId("ask-user-dock-minimize").click();
    await page.waitForTimeout(400);
    await phase(page, "question expand");
    await dock.getByTestId("ask-user-dock-expand").click();
    await page.waitForTimeout(400);
    await phase(page, "approval show");
    const approval = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, { headers, data: { session_id: sessionId, command: "printf '%s' '" + "Detail ".repeat(40) + "'" } });
    expect(approval.ok(), await approval.text()).toBe(true);
    const card = stage.getByTestId("tool-approval-card");
    await expect(card).toBeVisible();
    await phase(page, "approval minimize");
    await card.getByTestId("approval-card-minimize").click();
    await page.waitForTimeout(400);
    await phase(page, "approval expand");
    await card.getByTestId("approval-card-expand").click();
    await page.waitForTimeout(400);
    await phase(page, "approval resolve");
    await card.getByTestId("approval-approve-primary").click();
    await expect(card).toHaveCount(0);
    await page.waitForTimeout(400);
    const samples = await stopTrace(page);
    expect(samples.length).toBeGreaterThan(20);
    if (following) expect(Math.max(...samples.map((sample) => Math.abs(sample.tail)))).toBeLessThan(2);
    else {
      expect(samples.every((sample) => sample.key === reading.key)).toBe(true);
      expect(Math.max(...samples.map((sample) => Math.abs(sample.offset - reading.offset)))).toBeLessThan(2);
    }
  });
}

test("padding-only tail clearance changes reconcile without row growth", async ({ page, request }) => {
  const { stream } = await seed(page, request);
  for (const clearance of [41.125, 8, 24]) {
    await stream.locator(".den-chat-stream-body").evaluate((body, value) => { (body as HTMLElement).style.paddingBottom = `${value}px`; }, clearance);
    await expect.poll(() => tail(stream).then(Math.abs)).toBeLessThan(2);
  }
});
