import { ChangeSet, type ChangeSpec, type Text } from "@codemirror/state";
import { diffLines } from "diff";

// Broad replacements preserve unchanged text identities.
export function documentReplacementChanges(before: Text, changes: ChangeSet): ChangeSet {
  const result: ChangeSpec[] = [];
  changes.iterChanges((from, to, _newFrom, _newTo, inserted) => {
    const oldText = before.sliceString(from, to);
    const newText = inserted.toString();
    const parts = diffLines(oldText, newText, { maxEditLength: 2000, timeout: 25 });
    if (!parts) {
      appendReplacement(result, from, oldText, newText);
      return;
    }
    let position = from;
    let removed = "";
    let added = "";
    const flush = () => {
      appendReplacement(result, position, removed, added);
      position += removed.length;
      removed = added = "";
    };
    for (const part of parts) {
      if (part.removed) removed += part.value;
      else if (part.added) added += part.value;
      else { flush(); position += part.value.length; }
    }
    flush();
  });
  return ChangeSet.of(result, before.length);
}

function appendReplacement(result: ChangeSpec[], offset: number, before: string, after: string): void {
  let start = 0;
  while (start < before.length && start < after.length && before[start] === after[start]) start++;
  if (start > 0 && /[\uD800-\uDBFF]/.test(before[start - 1]!)) start--;
  let oldEnd = before.length;
  let newEnd = after.length;
  while (oldEnd > start && newEnd > start && before[oldEnd - 1] === after[newEnd - 1]) { oldEnd--; newEnd--; }
  if (oldEnd < before.length && /[\uDC00-\uDFFF]/.test(before[oldEnd]!)) { oldEnd++; newEnd++; }
  if (start !== oldEnd || start !== newEnd) result.push({ from: offset + start, to: offset + oldEnd, insert: after.slice(start, newEnd) });
}
