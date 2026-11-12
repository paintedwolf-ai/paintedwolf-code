export type BriefingInlinePart = {
  kind: "text" | "code";
  text: string;
};

/** Briefing prose supports inline code spans only. */
export function splitBriefingInline(text: string): BriefingInlinePart[] {
  const parts: BriefingInlinePart[] = [];
  const codeSpan = /`([^`\n]+)`/g;
  let cursor = 0;
  for (const match of text.matchAll(codeSpan)) {
    const start = match.index;
    const end = start + match[0].length;
    if (text[start - 1] === "`" || text[end] === "`") continue;
    if (start > cursor) parts.push({ kind: "text", text: text.slice(cursor, start) });
    parts.push({ kind: "code", text: match[1] ?? "" });
    cursor = end;
  }
  if (cursor < text.length) parts.push({ kind: "text", text: text.slice(cursor) });
  return parts.length > 0 ? parts : [{ kind: "text", text }];
}

/** The characters a reveal animates: code spans without their backticks. */
export function briefingRevealText(text: string): string {
  return splitBriefingInline(text).map((part) => part.text).join("");
}
