// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import {
  accessibleName,
  findTarget,
  isDisabled,
  isVisible,
  pointerReach,
  resolveByAccessibleName,
  resolveByLabel,
  resolveByRole,
  setNativeValue,
} from "./core.ts";
import { installLycaonDriver, MAX_SNAPSHOT_NODES, readSnapshot } from "./page-api.ts";

function markVisible(el: HTMLElement): void {
  Object.defineProperty(el, "offsetParent", { configurable: true, get: () => document.body });
}

describe("semantic-driver core", () => {
  afterEach(() => {
    document.body.replaceChildren();
  });

  it("setNativeValue updates controlled inputs via the native setter", () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    let seen = "";
    input.addEventListener("input", () => {
      seen = input.value;
    });
    setNativeValue(input, "hello");
    expect(input.value).toBe("hello");
    expect(seen).toBe("hello");
    input.remove();
  });

  it("isVisible rejects detached elements", () => {
    const el = document.createElement("button");
    expect(isVisible(el)).toBe(false);
  });

  it("text finds a button whose child holds the name", () => {
    const btn = document.createElement("button");
    markVisible(btn);
    const span = document.createElement("span");
    span.textContent = "Save plan";
    markVisible(span);
    btn.appendChild(span);
    document.body.appendChild(btn);
    expect(findTarget({ text: "Save" })).toBe(btn);
    btn.remove();
  });

  it("installLycaonDriver exposes state/snapshot", () => {
    const api = installLycaonDriver();
    const state = api.state();
    expect(state).toHaveProperty("url");
    expect(state).toHaveProperty("interactive");
    const snap = api.snapshot();
    expect(snap).toHaveProperty("tree");
  });

  it("snapshot stops at the global node budget and reports truncation", () => {
	const root = document.createElement("main");
	markVisible(root);
	document.body.appendChild(root);
	let frontier: HTMLElement[] = [root];
	for (let depth = 0; depth < 5; depth += 1) {
		const next: HTMLElement[] = [];
		for (const parent of frontier) {
			for (let i = 0; i < 6; i += 1) {
				const branch = document.createElement("section");
				branch.textContent = `branch-${depth}-${i}`;
				markVisible(branch);
				parent.appendChild(branch);
				next.push(branch);
			}
		}
		frontier = next;
	}

    const snapshot = readSnapshot();
    expect(snapshot.node_count).toBeLessThanOrEqual(MAX_SNAPSHOT_NODES);
    expect(snapshot.truncated).toBe(true);
  });

  it("role+label finds a bare button by accessible name", () => {
    const btn = document.createElement("button");
    btn.textContent = "Check configuration";
    markVisible(btn);
    document.body.appendChild(btn);
    expect(findTarget({ role: "button", label: "Check configuration" })).toBe(btn);
    expect(resolveByRole("button", "Check configuration")).toBe(btn);
    expect(resolveByAccessibleName("Check configuration")).toBe(btn);
    btn.remove();
  });

  it("label finds a named button", () => {
    const btn = document.createElement("button");
    btn.textContent = "Check configuration";
    markVisible(btn);
    document.body.appendChild(btn);
    expect(findTarget({ label: "Check configuration" })).toBe(btn);
    btn.remove();
  });

  it("label finds an associated form control", () => {
    const lab = document.createElement("label");
    lab.htmlFor = "name";
    lab.textContent = "Name";
    markVisible(lab);
    const input = document.createElement("input");
    input.id = "name";
    markVisible(input);
    document.body.appendChild(lab);
    document.body.appendChild(input);
    expect(resolveByLabel("Name")).toBe(input);
    expect(findTarget({ label: "Name" })).toBe(input);
    expect(accessibleName(input)).toBe("Name");
    lab.remove();
    input.remove();
  });

  it("role+label prefers aria-label over text content", () => {
    const btn = document.createElement("button");
    btn.textContent = "Icon";
    btn.setAttribute("aria-label", "Check configuration");
    markVisible(btn);
    document.body.appendChild(btn);
    expect(findTarget({ role: "button", label: "Check configuration" })).toBe(btn);
    expect(accessibleName(btn)).toBe("Check configuration");
    btn.remove();
  });

  it("text finds a control by accessible name", () => {
    const btn = document.createElement("button");
    btn.textContent = "Icon";
    btn.setAttribute("aria-label", "Check configuration");
    markVisible(btn);
    document.body.appendChild(btn);
    expect(findTarget({ text: "Check configuration" })).toBe(btn);
    btn.remove();
  });

  it("aim with role+label reports locators when the target is missing", async () => {
    const api = installLycaonDriver();
    const result = await api.aim({ role: "button", label: "Missing control", timeout_ms: 150 });
    expect(result.ok).toBe(false);
    expect(result.error).toBe("target not found");
    expect(result.locators).toEqual({ role: "button", label: "Missing control" });
    expect(result.state).toHaveProperty("interactive");
  });

  it("finds and recognizes visible SVG interactive elements", () => {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    const rect = document.createElementNS("http://www.w3.org/2000/svg", "rect");
    rect.setAttribute("data-testid", "zone-bridge");
    rect.setAttribute("role", "button");
    rect.setAttribute("aria-label", "Bridge to clock");
    Object.defineProperty(rect, "getClientRects", {
      configurable: true,
      value: () => [{ left: 50, top: 50, width: 100, height: 80, right: 150, bottom: 130, x: 50, y: 50 }],
    });
    svg.appendChild(rect);
    document.body.appendChild(svg);

    expect(isVisible(rect)).toBe(true);
    expect(findTarget({ testid: "zone-bridge" })).toBe(rect);
    expect(findTarget({ role: "button", label: "Bridge to clock" })).toBe(rect);
    expect(accessibleName(rect)).toBe("Bridge to clock");
    svg.remove();
  });

  it("aims a pointer-transparent icon through its own button", async () => {
    const button = document.createElement("button");
    button.setAttribute("aria-label", "Close tab");
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("data-testid", "close-icon");
    svg.style.pointerEvents = "none";
    placeAt(svg, { left: 10, top: 10, width: 16, height: 16 });
    button.appendChild(svg);
    document.body.appendChild(button);

    const result = await withElementFromPointAsync(() => button, () =>
      installLycaonDriver().aim({ testid: "close-icon", timeout_ms: 150 }),
    );
    expect(result).toMatchObject({ ok: true, point: { x: 18, y: 18 }, target: { tag: "svg", testid: "close-icon" } });
  });

  it("reports a pointer-transparent icon covered by something else", async () => {
    const button = document.createElement("button");
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("data-testid", "covered-icon");
    svg.style.pointerEvents = "none";
    placeAt(svg, { left: 10, top: 10, width: 16, height: 16 });
    button.appendChild(svg);
    const overlay = document.createElement("div");
    overlay.id = "scrim";
    document.body.append(button, overlay);

    const result = await withElementFromPointAsync(() => overlay, () =>
      installLycaonDriver().aim({ testid: "covered-icon", timeout_ms: 150 }),
    );
    expect(result).toMatchObject({ ok: false, error: "target obscured", obscured_by: { tag: "div", id: "scrim" } });
  });

  it("lands on the first sampled point the pointer reaches", () => {
    const target = document.createElement("div");
    placeAt(target, { left: 100, top: 100, width: 100, height: 100 });
    const banner = document.createElement("div");
    banner.className = "sticky header";
    document.body.append(target, banner);

    // The banner covers the top half, including the centre line.
    const reach = withElementFromPoint((_x, y) => (y <= 150 ? banner : target), () => pointerReach(target));
    expect(reach).toEqual({ state: "receives", point: { x: 125, y: 175 }, points_receiving: 2, points_sampled: 5 });
  });

  it("names the element that covers every sampled point", () => {
    const target = document.createElement("button");
    placeAt(target, { left: 100, top: 100, width: 100, height: 100 });
    const overlay = document.createElement("div");
    overlay.id = "layerB";
    overlay.className = "scene-layer";
    document.body.append(target, overlay);

    const reach = withElementFromPoint(() => overlay, () => pointerReach(target));
    expect(reach).toEqual({
      state: "covered",
      point: { x: 150, y: 150 },
      covered_by: { tag: "div", id: "layerB", className: "scene-layer" },
      points_sampled: 5,
    });
  });

  it("reports the ancestor that clips an element out of reach", () => {
    const scroller = document.createElement("div");
    scroller.id = "list";
    scroller.style.overflow = "auto";
    stubBox(scroller, { left: 0, top: 0, width: 300, height: 200 });
    const row = document.createElement("div");
    placeAt(row, { left: 0, top: 400, width: 300, height: 40 });
    scroller.appendChild(row);
    document.body.appendChild(scroller);

    expect(pointerReach(row)).toEqual({ state: "clipped", clipped_by: { tag: "div", id: "list" } });
  });

  it("reports an element outside the viewport and one that renders no box", () => {
    const below = document.createElement("div");
    placeAt(below, { left: 0, top: 5_000, width: 100, height: 20 });
    const empty = document.createElement("div");
    document.body.append(below, empty);

    expect(pointerReach(below)).toEqual({ state: "outside_viewport" });
    expect(pointerReach(empty)).toEqual({ state: "not_rendered" });
  });

  it("treats controls inside a disabled fieldset as disabled", () => {
    const fieldset = document.createElement("fieldset");
    fieldset.disabled = true;
    const input = document.createElement("input");
    fieldset.appendChild(input);
    document.body.appendChild(fieldset);

    expect(isDisabled(input)).toBe(true);
  });

  it("selects an option by value or visible label and announces the change", async () => {
    const select = document.createElement("select");
    select.setAttribute("data-testid", "plan");
    for (const [value, label] of [["free", "Free"], ["pro", "Pro plan"]]) {
      const option = document.createElement("option");
      option.value = value!;
      option.textContent = label!;
      select.appendChild(option);
    }
    markVisible(select);
    document.body.appendChild(select);
    const changes: string[] = [];
    select.addEventListener("change", () => changes.push(select.value));

    const api = installLycaonDriver();
    expect(await api.select({ testid: "plan", value: "Pro plan" })).toMatchObject({
      ok: true,
      selected: { value: "pro", label: "Pro plan" },
    });
    expect(await api.select({ testid: "plan", value: "free" })).toMatchObject({ ok: true });
    expect(await api.select({ testid: "plan", value: "Enterprise" })).toMatchObject({
      ok: false,
      error: "no matching option",
      options: [{ value: "free", label: "Free" }, { value: "pro", label: "Pro plan" }],
    });
    expect(changes).toEqual(["pro", "free"]);
  });
});

type BoxInit = { left: number; top: number; width: number; height: number };

function boxOf({ left, top, width, height }: BoxInit) {
  return { left, top, width, height, right: left + width, bottom: top + height, x: left, y: top };
}

function stubBox(el: Element, init: BoxInit): void {
  Object.defineProperty(el, "getBoundingClientRect", { configurable: true, value: () => boxOf(init) });
}

/** Gives an element a rendered box at a fixed viewport position. */
function placeAt(el: Element, init: BoxInit): void {
  Object.defineProperty(el, "getClientRects", { configurable: true, value: () => [boxOf(init)] });
  stubBox(el, init);
  if (el instanceof HTMLElement) markVisible(el);
}

function withElementFromPoint<T>(hit: (x: number, y: number) => Element | null, run: () => T): T {
  const original = document.elementFromPoint;
  document.elementFromPoint = hit;
  try {
    return run();
  } finally {
    document.elementFromPoint = original;
  }
}

async function withElementFromPointAsync<T>(hit: (x: number, y: number) => Element | null, run: () => Promise<T>): Promise<T> {
  const original = document.elementFromPoint;
  document.elementFromPoint = hit;
  try {
    return await run();
  } finally {
    document.elementFromPoint = original;
  }
}
