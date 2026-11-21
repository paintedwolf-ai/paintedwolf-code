function displayLine(value: string): string | undefined {
  for (const line of value.split("\n")) {
    const trimmed = line.trim();
    if (trimmed) return trimmed;
  }
  return undefined;
}

export function titleArgument(value: unknown): string | undefined {
  if (typeof value === "string" && value.trim()) return displayLine(value);
  if (!Array.isArray(value)) return undefined;
  const labels: string[] = [];
  for (const entry of value) {
    if (typeof entry === "string") {
      const line = displayLine(entry);
      if (line) labels.push(line);
    } else if (entry && typeof entry === "object") {
      const from = typeof entry.from === "string" ? displayLine(entry.from) : undefined;
      const to = typeof entry.to === "string" ? displayLine(entry.to) : undefined;
      if (from) labels.push(to ? `${from} → ${to}` : from);
    }
  }
  const first = labels[0];
  if (labels.length < 2 || first === undefined) return first;
  const suffix = ` +${labels.length - 1} more`;
  return truncateTitle(first, 96 - suffix.length) + suffix;
}

export function truncateTitle(text: string, max = 96): string {
  const trimmed = text.trim();
  // Slice code points to keep characters intact.
  const chars = Array.from(trimmed);
  if (chars.length <= max) return trimmed;
  return `${chars.slice(0, max - 1).join("")}…`;
}

export function compositeToolTitle(tool: string, args: Record<string, unknown>): string | null {
  const text = (key: string) => titleArgument(args[key]) ?? "";
  const comparison = (a: string, b: string) => a && b ? pairTitle(a, " → ", b) : a || b;
  switch (tool) {
    case "diff": return comparison(text("path_a"), text("path_b"));
    case "git_compare": return comparison(text("base_ref"), text("head_ref"));
    case "command":
    case "verify": {
      if (text("command")) return text("command");
      const stages: string[] = [];
      if (Array.isArray(args.pipeline)) {
        for (const value of args.pipeline) {
          const line = typeof value === "string" ? displayLine(value) : undefined;
          if (line) stages.push(line);
        }
      }
      const suffix = stages.length > 2 ? ` +${stages.length - 2} ${stages.length === 3 ? "stage" : "stages"}` : "";
      const first = stages[0] ?? "";
      const second = stages[1];
      return second ? pairTitle(first, " | ", second, 96 - suffix.length) + suffix : first;
    }
    default: return null;
  }
}

function pairTitle(left: string, separator: string, right: string, limit = 96): string {
  const available = limit - Array.from(separator).length;
  let leftLimit = Math.floor(available / 2);
  let rightLimit = available - leftLimit;
  const leftLength = Array.from(left).length;
  const rightLength = Array.from(right).length;
  if (leftLength < leftLimit) rightLimit += leftLimit - leftLength;
  if (rightLength < rightLimit) leftLimit = available - rightLength;
  return truncateTitle(left, leftLimit) + separator + truncateTitle(right, rightLimit);
}
