/** Ampersands precede replacements that introduce entity references. */
const HTML_ESCAPES: ReadonlyArray<readonly [RegExp, string]> = [
  [/&/g, "&amp;"],
  [/</g, "&lt;"],
  [/>/g, "&gt;"],
  [/"/g, "&quot;"],
  [/'/g, "&#39;"],
];

/** Escapes text and quoted HTML attributes. */
export function escapeHtml(value: string): string {
  let out = value;
  for (const [pattern, replacement] of HTML_ESCAPES) {
    out = out.replace(pattern, replacement);
  }
  return out;
}
