/** Wire money is integer nano-dollars (`*_nano_usd`); these helpers keep it exact. */

export const NANO_PER_USD = 1_000_000_000;

export type NanoUsdFormat = {
  /**
   * `spend` (default): always cents; a positive amount under half a cent reads `<$0.01`.
   * `limit`: whole-dollar amounts drop the cents; zero or negative reads as unset.
   */
  style?: "spend" | "limit";
};

/** Formats integer nano-dollars as USD for display. */
export function formatNanoUsd(value: number | null | undefined, opts: NanoUsdFormat = {}): string {
  if (value == null || !Number.isSafeInteger(value)) return "—";
  const limit = opts.style === "limit";
  if (limit && value <= 0) return "—";
  const wholeDollars = value % NANO_PER_USD === 0;
  const format = new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: limit && wholeDollars ? 0 : 2,
    maximumFractionDigits: 2,
  });
  if (!limit && value > 0 && value < NANO_PER_USD / 200) return `<${format.format(0.01)}`;
  // Round to whole cents in integers first; cents / 100 then formats exactly at two places.
  const cents = Math.round(value / (NANO_PER_USD / 100));
  return format.format(cents / 100);
}
