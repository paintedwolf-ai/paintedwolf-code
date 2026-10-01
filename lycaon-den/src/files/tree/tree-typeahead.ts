import type { SourceTreeFrame } from "../../api/types.ts";

/** Walk bounded pages of the expanded tree without retaining its full listing. */
export async function findTreePrefix(options: {
  prefix: string;
  from: number;
  count: number;
  signal: AbortSignal;
  frame: (offset: number, limit: number, signal: AbortSignal) => Promise<SourceTreeFrame>;
}): Promise<number | null> {
  const { count, signal } = options;
  if (!count) return null;
  const start = ((options.from % count) + count) % count;
  for (const [from, to] of [[start, count], [0, start]]) {
    let offset = from!;
    while (offset < to!) {
      signal.throwIfAborted();
      const page = await options.frame(offset, Math.min(200, to! - offset), signal);
      signal.throwIfAborted();
      for (let i = 0; i < page.rows.length; i++) {
        const index = page.span.start + i;
        const row = page.rows[i]!;
        if (index >= offset && index < to! && (row.kind === "file" || row.kind === "directory") &&
          row.name.toLocaleLowerCase().startsWith(options.prefix.toLocaleLowerCase())) return index;
      }
      if (page.span.end <= offset) break;
      offset = page.span.end;
    }
  }
  return null;
}
