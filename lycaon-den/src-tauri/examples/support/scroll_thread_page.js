// Page side of the scroll-thread invariants, evaluated in the harness Den.
window.__scrollThread = {
  stream() {
    return document.querySelector(".den-scrollport__viewport.den-chat-stream");
  },

  /** A chat long enough to scroll, ending in a turn with tool cards. */
  async seed() {
    const t0 = performance.now();
    // A seed that stalls reports the last step it reached.
    const mark = (step) => { this.progress = `${step} at ${Math.round(performance.now() - t0)} ms`; };
    mark("waiting for the harness API");
    for (let i = 0; i < 120 && !window.__harness; i++) await new Promise((r) => setTimeout(r, 250));
    if (!window.__harness) throw new Error("harness API missing");
    mark("opening the project");
    await __harness.openProject("Harness");
    mark("draining pending completions");
    // Earlier runs can leave completions pending; answer them before scripting this one.
    await __harness.llm.auto("ok");
    await new Promise((r) => setTimeout(r, 2500));
    await __harness.llm.manual();
    mark("opening a session");
    await __harness.newSession();
    const step = async (reply) => {
      await __harness.llm.pending(20000);
      await __harness.llm.respond(reply);
    };
    for (let i = 0; i < 5; i++) {
      mark(`answering question ${i + 1}`);
      await __harness.sendPrompt(`Question ${i + 1}: tell me about the layout.`);
      await step({ text: "The layout uses a spacing scale and an icon set. ".repeat(30) });
      await __harness.waitForIdle(30000);
    }
    mark("running the tool turn");
    await __harness.sendPrompt("Can we improve the interface?");
    await step({
      toolCalls: [
        { id: "b1", name: "read", args: { path: "README.md" } },
        { id: "b2", name: "read", args: { path: "main.go" } },
        { id: "b3", name: "read", args: { path: "go.mod" } },
      ],
    });
    await step({ text: "Done." });
    await __harness.waitForIdle(30000);
    mark("waiting for transcript rows");
    for (let i = 0; i < 60 && document.querySelectorAll(".transcript-viewport-row").length < 3; i++) {
      await new Promise((r) => setTimeout(r, 250));
    }
    await new Promise((r) => setTimeout(r, 1000));
    return document.querySelectorAll(".transcript-viewport-row").length;
  },

  /**
   * Extends the chat by `turns` answers of varied shape (prose, lists, code), so it is long
   * enough to virtualize and rows mount away from their estimates. Returns the scroll extent.
   */
  async extend(turns) {
    const step = async (reply) => {
      await __harness.llm.pending(20000);
      await __harness.llm.respond(reply);
    };
    const shapes = [
      (i) => `Step ${i} walks through the setup.\n\n` + "The profile holds the team and the entitlements it grants. ".repeat(12),
      (i) => `Checklist ${i}:\n\n` + Array.from({ length: 8 }, (_, n) => `- Item ${n + 1}: confirm the signing identity and the provisioning profile match.`).join("\n"),
      (i) => `Run this for part ${i}:\n\n\`\`\`sh\n` + Array.from({ length: 10 }, (_, n) => `security cms -D -i profile-${n}.provisionprofile | plutil -p - | grep -E 'Expiration'`).join("\n") + "\n```\n\nThen verify the output.",
    ];
    for (let i = 0; i < turns; i++) {
      await __harness.sendPrompt(`Part ${i + 1}: walk me through the next step.`);
      await step({ text: shapes[i % shapes.length](i + 1) });
      await __harness.waitForIdle(30000);
    }
    await new Promise((r) => setTimeout(r, 1500));
    return this.stream().scrollHeight;
  },

  /**
   * Records what the main thread sees and does from here on: scroll events with the extent at
   * each, and every offset Den writes. A failing check prints it.
   */
  journal() {
    const v = this.stream();
    if (!this.entries) {
      this.entries = [];
      const t0 = performance.now();
      const log = (entry) => {
        this.entries.push(`${Math.round(performance.now() - t0)}ms ${entry}`);
        if (this.entries.length > 400) this.entries.splice(0, this.entries.length - 400);
      };
      this.log = log;
      v.addEventListener("scroll", () => log(`scroll top=${v.scrollTop} height=${v.scrollHeight}`), { passive: true, capture: true });
      v.addEventListener("wheel", (e) => log(`wheel dy=${e.deltaY}`), { passive: true, capture: true });
      const top = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTop");
      Object.defineProperty(v, "scrollTop", {
        configurable: true,
        get() { return top.get.call(this); },
        set(value) { log(`write top=${value} (was ${top.get.call(this)}, height=${this.scrollHeight})`); top.set.call(this, value); },
      });
      const by = v.scrollBy.bind(v);
      v.scrollBy = (...args) => { log(`write by=${JSON.stringify(args)}`); return by(...args); };
    }
    return this.entries.splice(0).join("\n");
  },

  /** What the main thread holds: offset, extent, and the viewport's box in window CSS pixels. */
  geometry() {
    const v = this.stream();
    const r = v.getBoundingClientRect();
    return {
      top: v.scrollTop,
      height: v.scrollHeight,
      client: v.clientHeight,
      width: v.clientWidth,
      left: r.left,
      rectTop: r.top,
      rectWidth: r.width,
      rectHeight: r.height,
    };
  },

  /**
   * Makes the newest row re-measure on each scroll near the end while wheel input arrives, so the
   * range grows and contracts at its end under the stream, as rows mounting at their estimates do.
   */
  flapTail(on) {
    const v = this.stream();
    if (this.flap) {
      v.removeEventListener("scroll", this.flap);
      v.removeEventListener("wheel", this.flap.wheel, true);
      this.flap.row.style.paddingBottom = "";
      this.flap = undefined;
    }
    if (!on) return true;
    const row = [...v.querySelectorAll(".transcript-viewport-row")].at(-1);
    if (!row) return false;
    let n = 0;
    let wheelAt = -Infinity;
    const flap = () => {
      // Rows settle once the reader stops; they do not re-measure against Den's own writes.
      if (performance.now() - wheelAt > 100) return;
      if (v.scrollTop < v.scrollHeight - v.clientHeight - 600) return;
      n += 1;
      row.style.paddingBottom = n % 2 ? "60px" : "0px";
      this.log?.(`flap ${row.style.paddingBottom}`);
    };
    flap.row = row;
    flap.wheel = () => { wheelAt = performance.now(); };
    v.addEventListener("wheel", flap.wheel, { passive: true, capture: true });
    v.addEventListener("scroll", flap, { passive: true });
    this.flap = flap;
    return true;
  },

  /** Collapsed activity updates must not move a reader already following the tail. */
  async updateCollapsedActivity() {
    const v = this.stream();
    const card = [...v.querySelectorAll(".den-activity-span:not([open])")].at(-1);
    const hint = card?.querySelector(".den-activity-span-hint");
    if (!hint) throw new Error("collapsed activity missing");
    const text = hint.textContent;
    const failures = [];
    const initial = v.scrollTop;
    try {
      for (let update = 0; update < 60; update++) {
        hint.textContent = `Updated action ${String(update).padStart(2, "0")}`;
        await new Promise(requestAnimationFrame);
        if (Math.abs(v.scrollTop - initial) > 1) {
          failures.push(`update ${update}: offset ${v.scrollTop}, expected ${initial}`);
        }
      }
    } finally {
      hint.textContent = text;
    }
    return failures;
  },

  /** Grows the newest row every frame for `ms`, as a streaming reply does. */
  streamTail(ms) {
    const row = [...this.stream().querySelectorAll(".transcript-viewport-row")].at(-1);
    if (!row) return false;
    const body = document.createElement("div");
    row.append(body);
    const end = performance.now() + ms;
    const grow = () => {
      body.style.height = `${(parseFloat(body.style.height) || 0) + 6}px`;
      this.log?.(`stream +6 height=${this.stream().scrollHeight}`);
      if (performance.now() < end) requestAnimationFrame(grow);
      else setTimeout(() => body.remove(), 2000);
    };
    requestAnimationFrame(grow);
    return true;
  },

  /**
   * Arms every mounted row to settle once, `delta` px from the height it holds now, the first time
   * it lies wholly above the reader during a scroll, as a row mounting at its estimate does while
   * the reader scrolls up into it. Negative settles shorter. Each settle moves the reader the same
   * way, so an offset lost to one is never repaid by the next. `0` disarms and restores the rows.
   */
  settleAbove(delta) {
    const v = this.stream();
    if (this.above) {
      v.removeEventListener("scroll", this.above);
      for (const row of this.above.rows) row.style.paddingTop = "";
      this.above = undefined;
    }
    if (!delta) return true;
    const rows = new Set(v.querySelectorAll(".transcript-viewport-row"));
    // Rows that settle shorter mount tall, before any glide starts.
    if (delta < 0) for (const row of rows) row.style.paddingTop = `${-delta}px`;
    const pending = new Set(rows);
    const above = () => {
      const top = v.getBoundingClientRect().top;
      const row = [...pending].filter((r) => r.isConnected && r.getBoundingClientRect().bottom < top).at(-1);
      if (!row) return;
      pending.delete(row);
      row.style.paddingTop = delta < 0 ? "" : `${delta}px`;
      this.log?.(`settle above ${delta}px`);
    };
    above.rows = rows;
    v.addEventListener("scroll", above, { passive: true });
    this.above = above;
    return true;
  },

  /** A pointer-shaped press on the newest visible disclosure; returns its label. */
  pressNewestDisclosure() {
    const v = this.stream();
    const vr = v.getBoundingClientRect();
    const el = [...v.querySelectorAll("summary, [aria-expanded]")]
      .filter((e) => {
        const r = e.getBoundingClientRect();
        return r.top > vr.top && r.bottom < vr.bottom;
      })
      .at(-1);
    if (!el) return "none";
    const r = el.getBoundingClientRect();
    const init = { bubbles: true, cancelable: true, detail: 1, clientX: r.left + 40, clientY: r.top + r.height / 2, view: window };
    el.dispatchEvent(new PointerEvent("pointerdown", { ...init, pointerType: "mouse" }));
    el.dispatchEvent(new MouseEvent("mousedown", init));
    el.dispatchEvent(new PointerEvent("pointerup", { ...init, pointerType: "mouse" }));
    el.dispatchEvent(new MouseEvent("mouseup", init));
    el.dispatchEvent(new MouseEvent("click", init));
    return el.textContent.trim().replace(/\s+/g, " ").slice(0, 40);
  },
};
true;
