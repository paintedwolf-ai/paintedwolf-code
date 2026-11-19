const units = ["B", "KiB", "MiB", "GiB", "TiB"];
const number = new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 });

export function formatModelBytes(bytes: number): string {
  let value = Math.max(0, bytes);
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${number.format(value)} ${units[unit]}`;
}
