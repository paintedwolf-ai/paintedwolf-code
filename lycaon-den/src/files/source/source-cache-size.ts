/** Estimate retained data without serializing large source bodies a second time. */
export function sourceCacheBytes(value: unknown, limit: number): number {
  const pending: unknown[] = [value];
  const seen = new Set<object>();
  let bytes = 0;
  while (pending.length && bytes <= limit) {
    const item = pending.pop();
    if (typeof item === "string") bytes += item.length * 2 + 16;
    else if (item !== null && typeof item === "object") {
      if (seen.has(item)) continue;
      seen.add(item);
      bytes += 64;
      for (const key in item) {
        if (!Object.hasOwn(item, key)) continue;
        bytes += key.length * 2 + 16;
        if (bytes > limit) return bytes;
        pending.push((item as Record<string, unknown>)[key]);
      }
    } else bytes += 8;
  }
  return bytes;
}
