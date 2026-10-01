import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { AttentionRow } from "../../api/types.ts";
import { ATTENTION_BAND_CAP, AttentionBand } from "./AttentionBand.tsx";

const T0 = Date.parse("2026-07-27T12:00:00Z");

function row(over: Partial<AttentionRow> = {}): AttentionRow {
  return {
    session_id: "s1",
    project_id: "p1",
    class: "needs_you",
    reason: "checkpoint",
    since_at: new Date(T0).toISOString(),
    ...over,
  };
}

describe("AttentionBand", () => {
  // A permanent empty container teaches people to stop looking at it.
  it("renders nothing when nothing is blocked", () => {
    render(() => <AttentionBand rows={[]} onOpen={vi.fn()} />);
    expect(screen.queryByTestId("attention-band")).toBeNull();
  });

  it("renders nothing when work is merely running", () => {
    render(() => (
      <AttentionBand
        rows={[row({ class: "running", reason: "turn_running" })]}
        onOpen={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("attention-band")).toBeNull();
  });

  it("counts blocked chats in the heading, singular and plural", () => {
    const { unmount } = render(() => (
      <AttentionBand rows={[row()]} onOpen={vi.fn()} />
    ));
    expect(screen.getByTestId("attention-band").textContent).toContain(
      "1 chat is waiting on you",
    );
    unmount();

    render(() => (
      <AttentionBand
        rows={[row({ session_id: "a" }), row({ session_id: "b" })]}
        onOpen={vi.fn()}
      />
    ));
    expect(screen.getByTestId("attention-band").textContent).toContain(
      "2 chats are waiting on you",
    );
  });

  it("names the project a chat belongs to", () => {
    render(() => (
      <AttentionBand
        rows={[row({ title: "Fix auth", project_name: "Painted Wolf" })]}
        onOpen={vi.fn()}
        nowMs={T0}
      />
    ));
    const band = screen.getByTestId("attention-band").textContent ?? "";
    expect(band).toContain("Fix auth");
    expect(band).toContain("Painted Wolf");
  });

  it("says why each chat is blocked", () => {
    render(() => (
      <AttentionBand rows={[row({ reason: "ask" })]} onOpen={vi.fn()} nowMs={T0} />
    ));
    expect(screen.getByTestId("attention-band").textContent).toContain(
      "Waiting on your answer",
    );
  });

  it("shows a coarse age", () => {
    render(() => (
      <AttentionBand rows={[row()]} onOpen={vi.fn()} nowMs={T0 + 25 * 60_000} />
    ));
    expect(screen.getByTestId("attention-band").textContent).toContain("25m");
  });

  it("opens the chat it was clicked on", () => {
    const onOpen = vi.fn();
    render(() => (
      <AttentionBand rows={[row({ session_id: "target" })]} onOpen={onOpen} />
    ));
    fireEvent.click(screen.getByTestId("attention-band-row"));
    expect(onOpen).toHaveBeenCalledOnce();
    expect(onOpen.mock.calls[0]?.[0]?.session_id).toBe("target");
  });

  it("falls back to a placeholder for an untitled chat", () => {
    render(() => <AttentionBand rows={[row({ title: undefined })]} onOpen={vi.fn()} />);
    expect(screen.getByTestId("attention-band").textContent).toContain(
      "Untitled chat",
    );
  });

  it("caps the rows and says how many are hidden", () => {
    const rows = Array.from({ length: ATTENTION_BAND_CAP + 3 }, (_, i) =>
      row({ session_id: `s${i}` }),
    );
    render(() => <AttentionBand rows={rows} onOpen={vi.fn()} nowMs={T0} />);
    expect(screen.getAllByTestId("attention-band-row")).toHaveLength(
      ATTENTION_BAND_CAP,
    );
    expect(screen.getByTestId("attention-band-more").textContent).toBe("and 3 more");
  });

  it("omits the overflow line when everything fits", () => {
    render(() => <AttentionBand rows={[row()]} onOpen={vi.fn()} />);
    expect(screen.queryByTestId("attention-band-more")).toBeNull();
  });

  it("spells the row out as a sentence for screen readers", () => {
    render(() => (
      <AttentionBand
        rows={[row({ title: "Fix auth", project_name: "Painted Wolf", reason: "ask" })]}
        onOpen={vi.fn()}
      />
    ));
    expect(screen.getByTestId("attention-band-row").getAttribute("aria-label")).toBe(
      "Fix auth in Painted Wolf — Waiting on your answer",
    );
  });
});
