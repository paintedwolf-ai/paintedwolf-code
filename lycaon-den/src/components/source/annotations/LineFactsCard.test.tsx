import { cleanup, render, screen, waitFor, fireEvent } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal, Show } from "solid-js";
import * as briefingEvidence from "../../../briefing/briefing-evidence.ts";
import { BRIEFING_DEBOUNCE_MS, resetBriefingLiveForTests, setBriefingClient, setBriefingClockForTests } from "../../../briefing/briefing-live.ts";
import { AnchoredSurface } from "../../primitives/AnchoredSurface.tsx";
import { LineFactsCard, type LineFactsCardState } from "./LineFactsCard.tsx";
import type { LineFactRow } from "./line-facts.ts";

vi.mock("../../../settings/editor/file-summary-settings.ts", () => ({
  fileSummariesEnabled: () => true,
  fileSummariesSettingKnown: () => true,
}));

const row = (id: string, extra: Partial<LineFactRow> = {}): LineFactRow => ({
  key: id,
  title: id,
  fact: "Edited lines 4–6 · turn 12",
  target: { kind: "chat", sessionId: id, toolCallId: `tool-${id}` },
  ...extra,
});
function fixture(rows: LineFactRow[] = [row("first"), row("second")]) {
  const editor = document.createElement("div");
  editor.className = "cm-editor";
  const anchor = document.createElement("div");
  const handle = anchor.appendChild(document.createElement("button"));
  handle.className = "files-line-gutter__facts";
  const code = document.createElement("div");
  code.tabIndex = 0;
  editor.append(anchor, code);
  document.body.append(editor);
  anchor.getBoundingClientRect = () => new DOMRect(10, 20, 56, 20);
  const [state, setState] = createSignal<LineFactsCardState>({ anchor, line: 4, path: "a.go", enter: false,
    facts: { now: [], changed: rows, findings: [], count: rows.length } });
  const handlers = { onActivate: vi.fn(), onDismiss: vi.fn(), onReturnFocus: vi.fn() };
  render(() => <LineFactsCard state={state()} {...handlers} />);
  return { editor, anchor, handle, code, state, setState, handlers };
}
afterEach(() => { cleanup(); resetBriefingLiveForTests(); document.body.replaceChildren(); vi.restoreAllMocks(); });
describe("line facts card", () => {
  it("opens the selected contributor at the originating line", () => {
    const { handlers } = fixture();
    fireEvent.click(screen.getByRole("button", { name: /second/ }));
    expect(handlers.onActivate).toHaveBeenCalledWith(row("second"), 4);
  });
  it("keeps focus and the surface when names refresh", async () => {
    const { anchor, setState, handlers } = fixture();
    const surface = screen.getByRole("dialog"), button = screen.getByRole("button", { name: /second/ });
    button.focus();
    setState(old => ({ ...old, facts: { ...old.facts, changed: old.facts.changed.map(item => ({ ...item, title: `${item.title} renamed` })) } }));
    await waitFor(() => expect(screen.getByText("second renamed")).toBeTruthy());
    expect(screen.getByRole("dialog")).toBe(surface);
    expect(document.activeElement).toBe(button);
    fireEvent.pointerDown(anchor);
    expect(handlers.onDismiss).not.toHaveBeenCalled();
    fireEvent.pointerDown(document.body);
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
  });
  it("anchors to the right, supports row navigation, and returns focus on Escape from inside", async () => {
    const { handlers } = fixture();
    const dialog = screen.getByRole("dialog");
    await waitFor(() => expect(dialog.style.left).toBe("70px"));
    expect(dialog.dataset.side).toBe("right");
    const first = screen.getByRole("button", { name: /first/ }), second = screen.getByRole("button", { name: /second/ });
    first.focus(); fireEvent.keyDown(first, { key: "ArrowDown" });
    expect(document.activeElement).toBe(second);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(handlers.onReturnFocus).toHaveBeenCalledOnce();
    expect(handlers.onDismiss).not.toHaveBeenCalled();
  });
  it("dismisses on Escape when focus is outside the card and yields to layers in front", () => {
    const { code, handlers } = fixture();
    const [frontOpen, setFrontOpen] = createSignal(true);
    let popupOpen = true;
    const composer = document.body.appendChild(document.createElement("textarea"));
    composer.addEventListener("keydown", event => {
      if (event.key === "Escape" && popupOpen) {
        popupOpen = false;
        event.preventDefault();
      }
    });
    const findBar = document.body.appendChild(document.createElement("div"));
    findBar.className = "den-find-bar";
    const findInput = findBar.appendChild(document.createElement("input"));

    render(() => (
      <Show when={frontOpen()}>
        <AnchoredSurface anchor={() => ({ x: 40, y: 40 })} role="dialog" ariaLabel="Front" onDismiss={() => setFrontOpen(false)}>
          <div>Front</div>
        </AnchoredSurface>
      </Show>
    ));

    code.focus();
    fireEvent.keyDown(code, { key: "Escape" });
    expect(frontOpen()).toBe(false);
    expect(handlers.onDismiss).not.toHaveBeenCalled();

    composer.focus();
    fireEvent.keyDown(composer, { key: "Escape" });
    expect(popupOpen).toBe(false);
    expect(handlers.onDismiss).not.toHaveBeenCalled();

    findInput.focus();
    fireEvent.keyDown(findInput, { key: "Escape" });
    expect(handlers.onDismiss).not.toHaveBeenCalled();

    composer.focus();
    fireEvent.keyDown(composer, { key: "Escape" });
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
    expect(handlers.onReturnFocus).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(composer);
  });
  it("ignores pointer entry and exit, dismisses on scroll, and dismisses when focus leaves editor and card", () => {
    const { code, handlers } = fixture();
    const dialog = screen.getByRole("dialog");
    const first = screen.getByRole("button", { name: /first/ });
    fireEvent.mouseEnter(dialog);
    fireEvent.mouseLeave(dialog);
    expect(handlers.onDismiss).not.toHaveBeenCalled();
    expect(handlers.onReturnFocus).not.toHaveBeenCalled();
    first.focus();
    fireEvent.focusOut(first, { relatedTarget: code });
    expect(handlers.onDismiss).not.toHaveBeenCalled();
    const outside = document.body.appendChild(document.createElement("button"));
    fireEvent.focusOut(first, { relatedTarget: outside });
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
    handlers.onDismiss.mockClear();
    fireEvent.scroll(window);
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
  });
  it("renders fact, relative time, and tool in order", () => {
    const full = row("with-time", { when: "17m ago", tool: "edit" });
    const plain = row("without-time");
    fixture([full, plain]);
    const facts = [...document.querySelectorAll<HTMLElement>(".files-line-facts__fact")].map(el => el.textContent);
    expect(facts).toEqual([
      [full.fact, full.when, full.tool].join(" · "),
      plain.fact,
    ]);
  });
  it("enters an already-positioned card when the gutter requests keyboard entry", async () => {
    const { handle, setState } = fixture();
    await waitFor(() => expect(screen.getByRole("dialog").style.left).toBe("70px"));
    handle.focus();
    setState(state => ({ ...state, enter: true }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: /first/ })));
  });
  it("renders the change briefing alongside row metadata, returns focus on Escape, and dismisses when opening evidence", async () => {
    let time = 0, nextId = 0;
    const timers = new Map<number, { at: number; run: () => void }>();
    setBriefingClockForTests({
      now: () => time,
      setTimeout: (run, ms) => { const id = ++nextId; timers.set(id, { at: time + ms, run }); return id; },
      clearTimeout: id => { timers.delete(id as number); },
    });
    const summaryText = "tightened config loader";
    setBriefingClient(() => ({
      requestBriefing: async (_projectId, req) => ({
        profile_id: "source_step", profile_version: "1", target_key: String((req.target_ref as { target_key: string }).target_key),
        scope_fingerprint: "f", target: {}, cache_epoch: "1", revision: "1", status: "idle", pending_sections: [],
        outcome: { state: "unknown" }, error_code: "", error_message: "",
        result: {
          summary: [{
            section_id: "change", text: summaryText, manifest_hash: "a".repeat(64),
            source: "summarizer", reused: false, provider_id: "p", model: "m", evidence_quality: [], created_at: "2026-11-11T00:00:00Z",
          }],
          evidence_ref: { project_id: "proj-1", record_id: "r".repeat(64) },
        },
      }),
      getBriefingRecord: async () => { throw new Error("unused"); },
    }));
    const openEvidence = vi.spyOn(briefingEvidence, "openBriefingEvidence").mockImplementation(() => {});

    const changedRow = row("first", { when: "5m ago", tool: "command", effectId: "eff-1" });
    const { handle, setState, handlers } = fixture([changedRow]);
    setState(old => ({ ...old, projectId: "proj-1" }));
    time += BRIEFING_DEBOUNCE_MS;
    for (const [id, timer] of [...timers]) if (timer.at <= time) { timers.delete(id); timer.run(); }

    await waitFor(() => expect(screen.getByTestId("line-facts-briefing").textContent).toContain(summaryText));
    expect(document.querySelector(".files-line-facts__fact")?.textContent).toBe(
      [changedRow.fact, changedRow.when, changedRow.tool].join(" · "),
    );

    const evidence = screen.getByTestId("line-facts-evidence");
    evidence.focus();
    fireEvent.keyDown(evidence, { key: "Escape" });
    expect(handlers.onReturnFocus).toHaveBeenCalledOnce();
    expect(handlers.onDismiss).not.toHaveBeenCalled();

    fireEvent.click(evidence);
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
    expect(openEvidence).toHaveBeenCalledOnce();
    openEvidence.mock.calls[0]?.[1]?.focus?.();
    expect(document.activeElement).toBe(handle);
  });
});
