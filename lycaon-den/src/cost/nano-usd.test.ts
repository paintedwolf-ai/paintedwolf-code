import { describe, expect, it } from "vitest";
import { formatNanoUsd, NANO_PER_USD } from "./nano-usd.ts";

const usd = (amount: number) =>
  new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(amount);
const wholeUsd = (amount: number) =>
  new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", minimumFractionDigits: 0, maximumFractionDigits: 2 }).format(amount);

describe("formatNanoUsd", () => {
  it("formats spend in cents and keeps sub-cent spend visible", () => {
    expect(formatNanoUsd(12_340_000_000)).toBe(usd(12.34));
    expect(formatNanoUsd(0)).toBe(usd(0));
    expect(formatNanoUsd(4_000_000)).toBe(`<${usd(0.01)}`);
    expect(formatNanoUsd(5_000_000)).toBe(usd(0.01));
    expect(formatNanoUsd(-2_500_000_000)).toBe(usd(-2.5));
  });

  it("rounds half a cent up without float drift", () => {
    // 1.005 is 1.00499999… as a float; integer cent rounding keeps it 1.01.
    expect(formatNanoUsd(1_005_000_000)).toBe(usd(1.01));
    expect(formatNanoUsd(1_004_999_999)).toBe(usd(1));
  });

  it("formats limits without cents when whole and treats non-positive limits as unset", () => {
    expect(formatNanoUsd(25 * NANO_PER_USD, { style: "limit" })).toBe(wholeUsd(25));
    expect(formatNanoUsd(2_500_000_000, { style: "limit" })).toBe(usd(2.5));
    expect(formatNanoUsd(0, { style: "limit" })).toBe("—");
  });

  it("renders missing or non-integer values as a dash", () => {
    expect(formatNanoUsd(null)).toBe("—");
    expect(formatNanoUsd(undefined)).toBe("—");
    expect(formatNanoUsd(Number.NaN)).toBe("—");
    expect(formatNanoUsd(1.5)).toBe("—");
  });
});
