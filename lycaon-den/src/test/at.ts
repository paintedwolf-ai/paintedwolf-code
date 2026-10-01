/**
 * Indexed access for test fixtures and rendered queries. Narrows `T | undefined`
 * at the access site and throws on a missing index or key.
 */

export function at<T>(xs: ArrayLike<T>, index: number): T {
  const value = xs[index];
  if (value === undefined) {
    throw new Error(`index ${index} is out of range (length ${xs.length})`);
  }
  return value;
}

export function keyAt<T>(record: Readonly<Record<string, T>>, key: string): T {
  const value = record[key];
  if (value === undefined) {
    throw new Error(`key ${JSON.stringify(key)} is missing`);
  }
  return value;
}

export function required<T>(value: T | null | undefined): T {
  if (value === undefined || value === null) throw new Error("Expected a fixture value.");
  return value;
}
