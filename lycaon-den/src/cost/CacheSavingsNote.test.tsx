import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { CacheSavingsNote } from "./CacheSavingsNote.tsx";

describe("CacheSavingsNote", () => {
  it("does not invent a comparison without reported cache use", () => {
    render(() => <CacheSavingsNote totals={{ prompt: 2000, completion: 0, cache_read: 2000 }} />);
    expect(screen.queryByTestId("cost-cache-comparison")).toBeNull();
  });

  it("shows unavailable when every cache token lacks a comparison rate", () => {
    render(() => <CacheSavingsNote totals={{ prompt: 100, completion: 0, cache_read: 100 }} savings={{ estimated_nano_usd: 0, unpriced_tokens: 100 }} />);
    expect(screen.getByTestId("cost-cache-comparison").textContent).toContain("Cache comparison unavailable");
    expect(screen.getByTestId("cost-cache-comparison").textContent).not.toContain("$0");
  });

  it("explains a write premium without subtracting it from spend", () => {
    render(() => <CacheSavingsNote totals={{ prompt: 2000, completion: 0, cache_read: 2000 }} savings={{ estimated_nano_usd: -250_000_000, unpriced_tokens: 0 }} />);
    expect(screen.getByTestId("cost-cache-comparison").textContent).toMatch(
      /\$0.25 more than ordinary input pricing for the same tokens, including cache-write premiums/,
    );
  });

  it("discloses missing comparison rates independently of the known benefit", () => {
    render(() => <CacheSavingsNote totals={{ prompt: 2000, completion: 0, cache_read: 2000 }} savings={{ estimated_nano_usd: 500_000_000, unpriced_tokens: 1200 }} />);
    const text = screen.getByTestId("cost-cache-comparison").textContent;
    expect(text).toContain("$0.50 less");
    expect(text).toMatch(/Partial comparison: .* cache tokens have no comparison price/);
    expect(text).not.toContain("lower bound");
  });
});
