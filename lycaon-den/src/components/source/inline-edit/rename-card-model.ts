/**
 * Pure model for the rename card: whole-word counts, collision warning, and
 * input validation. No host I/O.
 */

export type RenameScope = "file" | "everywhere";

const WORD_CHAR = /[A-Za-z0-9_$]/;

/** Count whole-word, case-sensitive occurrences of `word` in `text`. */
export function countWholeWordOccurrences(text: string, word: string): number {
  if (!word) return 0;
  let count = 0;
  let from = 0;
  for (;;) {
    const at = text.indexOf(word, from);
    if (at < 0) break;
    const beforeChar = at > 0 ? text[at - 1]! : "";
    const afterChar =
      at + word.length < text.length ? text[at + word.length]! : "";
    const boundedLeft = !beforeChar || !WORD_CHAR.test(beforeChar);
    const boundedRight = !afterChar || !WORD_CHAR.test(afterChar);
    if (boundedLeft && boundedRight) count++;
    from = at + word.length;
  }
  return count;
}

/**
 * Valid rename input: nonempty after trim, no whitespace. Language-specific
 * identifier rules stay with the engines that apply the rename.
 */
export function validRenameInput(name: string): boolean {
  const t = name.trim();
  return t.length > 0 && !/\s/.test(t);
}

/**
 * Warns when a distinct replacement already occurs in the file.
 */
export function renameCollisionWarning(
  docText: string,
  oldName: string,
  newName: string,
): string | null {
  const next = newName.trim();
  if (!next || next === oldName) return null;
  if (countWholeWordOccurrences(docText, next) === 0) return null;
  return `${next} already appears in this file — matches are not merged`;
}

export type RenameProjectCount = {
  matches: number;
  files: number;
  truncated: boolean;
};

/** "12 across 4 files" · "1 across 1 file" · "500+ across 20+ files". */
export function projectCountLabel(count: RenameProjectCount): string {
  const suffix = count.truncated ? "+" : "";
  const files = `${count.files}${suffix} file${count.files === 1 && !count.truncated ? "" : "s"}`;
  return `${count.matches}${suffix} across ${files}`;
}
