/**
 * Structural equality over plain data that returns at the first shared reference.
 *
 * Rebuilt transcript rows hold the same wire objects and strings as the last build, so
 * an unchanged row compares in a handful of reference checks and never serializes.
 */
export function sameWireValue(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (typeof a !== "object" || typeof b !== "object" || a === null || b === null) return false;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    for (let index = 0; index < a.length; index++) {
      if (!sameWireValue(a[index], b[index])) return false;
    }
    return true;
  }
  const left = a as Record<string, unknown>;
  const right = b as Record<string, unknown>;
  const keys = Object.keys(left);
  if (keys.length !== Object.keys(right).length) return false;
  for (const key of keys) {
    if (!Object.hasOwn(right, key) || !sameWireValue(left[key], right[key])) return false;
  }
  return true;
}
