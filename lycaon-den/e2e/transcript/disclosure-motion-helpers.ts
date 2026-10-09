import { expect, type Locator, type Page } from "@playwright/test";
import { LIVE_CHAT_STAGE_SELECTOR } from "../../shared/stage-selectors.ts";

export async function disclosureViewportState(summary: Locator) {
  return summary.evaluate((control) => {
    const viewport = control
      .closest(".den-shell-stage--chat")
      ?.querySelector<HTMLElement>(".den-chat-stream");
    const row = control.closest<HTMLElement>(".transcript-viewport-row");
    const section = row?.closest<HTMLElement>(".den-chat-stream-inner");
    return {
      summaryTop: control.getBoundingClientRect().top,
      rowTop: row?.getBoundingClientRect().top ?? -1,
      virtualStart: Number(row?.dataset.virtualStart ?? -1),
      beforeRunway: Number(
        section?.querySelector<HTMLElement>(
          '[data-transcript-runway="before"]',
        )?.style.height.replace("px", "") ?? -1,
      ),
      afterRunway: Number(
        section?.querySelector<HTMLElement>(
          '[data-transcript-runway="after"]',
        )?.style.height.replace("px", "") ?? -1,
      ),
      scrollTop: viewport?.scrollTop ?? -1,
      scrollHeight: viewport?.scrollHeight ?? -1,
      clientHeight: viewport?.clientHeight ?? -1,
    };
  });
}

export async function disclosureCornerState(shell: Locator) {
  return shell.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      radius: style.borderRadius,
      clipPath: style.clipPath,
    };
  });
}

type DisclosureScrollTrace = {
  active: boolean;
  samples: Array<{ scrollTop: number; controlTop: number }>;
};

export async function startDisclosureScrollTrace(control: Locator) {
  await control.evaluate((element) => {
    const viewport = element
      .closest(".den-shell-stage--chat")
      ?.querySelector<HTMLElement>(".den-chat-stream");
    if (!viewport) throw new Error("chat viewport is missing");
    const trace: DisclosureScrollTrace = {
      active: true,
      samples: [
        {
          scrollTop: viewport.scrollTop,
          controlTop: element.getBoundingClientRect().top,
        },
      ],
    };
    (window as Window & { __denDisclosureScrollTrace?: DisclosureScrollTrace })
      .__denDisclosureScrollTrace = trace;
    const sample = () => {
      if (!trace.active) return;
      trace.samples.push({
        scrollTop: viewport.scrollTop,
        controlTop: element.getBoundingClientRect().top,
      });
      requestAnimationFrame(sample);
    };
    requestAnimationFrame(sample);
  });
}

export async function stopDisclosureScrollTrace(
  control: Locator,
): Promise<DisclosureScrollTrace> {
  return control.evaluate(() => {
    const target = window as Window & {
      __denDisclosureScrollTrace?: DisclosureScrollTrace;
    };
    const trace = target.__denDisclosureScrollTrace;
    if (!trace) throw new Error("disclosure scroll trace is missing");
    trace.active = false;
    delete target.__denDisclosureScrollTrace;
    return trace;
  });
}

export async function clickDisclosureWithoutReveal(control: Locator) {
  await control.dispatchEvent("pointerdown", {
    bubbles: true,
    cancelable: true,
    composed: true,
    button: 0,
    pointerId: 1,
    pointerType: "mouse",
  });
  await control.dispatchEvent("pointerup", {
    bubbles: true,
    cancelable: true,
    composed: true,
    button: 0,
    pointerId: 1,
    pointerType: "mouse",
  });
  await control.dispatchEvent("click", {
    bubbles: true,
    cancelable: true,
    composed: true,
    button: 0,
  });
}

export function expectStableScrollTrace(
  trace: DisclosureScrollTrace,
  keys: ReadonlyArray<"scrollTop" | "controlTop"> = [
    "scrollTop",
    "controlTop",
  ],
) {
  expect(trace.samples.length).toBeGreaterThan(2);
  for (const key of keys) {
    const values = trace.samples.map((sample) => sample[key]);
    expect(
      Math.max(...values) - Math.min(...values),
      JSON.stringify({ key, samples: trace.samples }),
    ).toBeLessThanOrEqual(1);
  }
}

export function expectNonOscillatingScrollTrace(trace: DisclosureScrollTrace) {
  expect(trace.samples.length).toBeGreaterThan(2);
  for (const key of ["scrollTop", "controlTop"] as const) {
    const directions = new Set<number>();
    for (let index = 1; index < trace.samples.length; index += 1) {
      const delta = trace.samples[index]![key] - trace.samples[index - 1]![key];
      if (Math.abs(delta) > 1) directions.add(Math.sign(delta));
    }
    expect(directions.size, JSON.stringify({ key, samples: trace.samples })).toBeLessThanOrEqual(1);
  }
}

export async function positionDisclosure(
  page: Page,
  control: Locator,
  alignment: "top" | "center" | "bottom",
) {
  const viewport = page
    .locator(
      `${LIVE_CHAT_STAGE_SELECTOR} .den-chat-stream`,
    )
    .first();
  const box = await viewport.boundingBox();
  if (!box) throw new Error("chat viewport is missing");
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const delta = await control.evaluate(
      (element, requestedAlignment) => {
        const viewport = element
          .closest(".den-shell-stage--chat")
          ?.querySelector<HTMLElement>(
            ".den-chat-stream",
          );
        if (!viewport) throw new Error("chat viewport is missing");
        const viewportBox = viewport.getBoundingClientRect();
        const controlBox = element.getBoundingClientRect();
        const inset = 12;
        const desiredTop =
          requestedAlignment === "top"
            ? viewportBox.top + inset
            : requestedAlignment === "bottom"
              ? viewportBox.bottom - controlBox.height - inset
              : viewportBox.top + (viewportBox.height - controlBox.height) / 2;
        return controlBox.top - desiredTop;
      },
      alignment,
    );
    if (Math.abs(delta) <= 2) break;
    await page.mouse.wheel(0, delta);
    await page.waitForTimeout(180);
  }
  // Reader scrolling pauses follow before the disclosure click.
  await page.mouse.wheel(0, -4);
  await page.waitForTimeout(180);
  await control.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
}

export async function toggleAndExpectControlled(
  control: Locator,
  shell: Locator,
  stream: Locator,
  opts?: { geometryAboveChanges?: boolean },
) {
  const before = await stream.evaluate((element) => element.scrollTop);
  const wasOpen = await shell.getAttribute("open");
  await startDisclosureScrollTrace(control);
  await clickDisclosureWithoutReveal(control);
  if (wasOpen === null) await expect(shell).toHaveAttribute("open", "");
  else await expect(shell).not.toHaveAttribute("open", "");
  await expect(shell).not.toHaveAttribute("data-animating", "true", {
    timeout: 2_000,
  });
  const trace = await stopDisclosureScrollTrace(control);
  if (wasOpen === null) {
    expectStableScrollTrace(
      trace,
      opts?.geometryAboveChanges ? ["scrollTop"] : undefined,
    );
    expect(
      Math.abs(
        (await stream.evaluate((element) => element.scrollTop)) - before,
      ),
      JSON.stringify({ before, after: await stream.evaluate((element) => element.scrollTop), control: await control.textContent(), trace }),
    ).toBeLessThanOrEqual(1);
  } else {
    expectNonOscillatingScrollTrace(trace);
  }
}
