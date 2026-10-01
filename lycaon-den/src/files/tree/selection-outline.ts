export type SelectionRectangle = { left: number; top: number; width: number | null; height: number };
export type SelectionEdge = { x1: number; y1: number; x2: number; y2: number };
type Interval = [number, number];

function subtract(intervals: Interval[], from: number, to: number): Interval[] {
  return intervals.flatMap(([start, end]) => {
    if (to <= start || from >= end) return [[start, end]] as Interval[];
    const remaining: Interval[] = [];
    if (from > start) remaining.push([start, from]);
    if (to < end) remaining.push([to, end]);
    return remaining;
  });
}

/** The perimeter of the rectangle union, with shared and covered edges removed. */
export function selectionOutline(rectangles: readonly SelectionRectangle[]): SelectionEdge[] {
  // Normalize subpixel measurements so shared edges coincide.
  const snap = (value: number) => Math.round(value * 1000) / 1000;
  const boxes = rectangles.filter(rect => rect.width !== null && rect.width > 0 && rect.height > 0)
    .map(rect => ({ x1: snap(rect.left), x2: snap(rect.left + rect.width!), y1: snap(rect.top), y2: snap(rect.top + rect.height) }));
  const groups = new Map<string, { horizontal: boolean; position: number; intervals: Interval[] }>();
  for (const box of boxes) {
    for (const [horizontal, positive] of [[true, false], [true, true], [false, false], [false, true]] as const) {
      const position = horizontal ? (positive ? box.y2 : box.y1) : (positive ? box.x2 : box.x1);
      let intervals: Interval[] = [horizontal ? [box.x1, box.x2] : [box.y1, box.y2]];
      for (const other of boxes) {
        if (other === box) continue;
        const near = horizontal ? other.y1 : other.x1, far = horizontal ? other.y2 : other.x2;
        const covers = positive ? near <= position && far > position : near < position && far >= position;
        if (covers) intervals = subtract(intervals, horizontal ? other.x1 : other.y1, horizontal ? other.x2 : other.y2);
      }
      const key = `${horizontal}:${position}`;
      const group = groups.get(key) ?? { horizontal, position, intervals: [] };
      group.intervals.push(...intervals);
      groups.set(key, group);
    }
  }
  return [...groups.values()].flatMap(({ horizontal, position, intervals }) => {
    const merged: Interval[] = [];
    for (const interval of intervals.sort((a, b) => a[0] - b[0])) {
      const last = merged[merged.length - 1];
      if (last && interval[0] <= last[1]) last[1] = Math.max(last[1], interval[1]);
      else merged.push([...interval]);
    }
    return merged.map(([from, to]) => horizontal
      ? { x1: from, y1: position, x2: to, y2: position }
      : { x1: position, y1: from, x2: position, y2: to });
  });
}
