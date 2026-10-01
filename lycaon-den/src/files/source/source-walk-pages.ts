import type {
  SourceCommandWindow,
  SourceGitChange,
  SourceWalkFile,
} from "../../api/types.ts";

/** Merges Git movements referenced across history pages. */
export function mergeSourceGitChanges(
  changes: readonly SourceGitChange[],
): SourceGitChange[] {
  const seen = new Set<string>();
  const out: SourceGitChange[] = [];
  for (const change of changes) {
    if (seen.has(change.id)) continue;
    seen.add(change.id);
    out.push(change);
  }
  return out;
}

/** Merges command windows referenced across history pages. */
export function mergeSourceCommandWindows(
  commands: readonly SourceCommandWindow[],
): SourceCommandWindow[] {
  const byId = new Map<string, SourceCommandWindow>();
  for (const command of commands) {
    if (!byId.has(command.id)) byId.set(command.id, command);
  }
  return [...byId.values()];
}

/** Commit rows use path identity; history rows use recorded file identity. */
export function mergeSourceWalkFiles(
  files: readonly SourceWalkFile[],
): SourceWalkFile[] {
  const byFile = new Map<
    string,
    { file: SourceWalkFile; effectIds: Set<string> }
  >();
  const out: SourceWalkFile[] = [];

  for (const file of files) {
    const key = file.commit ? `${file.root_id}\0${file.path}` : file.file_id;
    const held = byFile.get(key);
    if (!held) {
      const copy = { ...file, effects: [...file.effects] };
      byFile.set(key, {
        file: copy,
        effectIds: new Set(copy.effects.map((effect) => effect.id)),
      });
      out.push(copy);
      continue;
    }
    for (const effect of file.effects) {
      if (held.effectIds.has(effect.id)) continue;
      held.effectIds.add(effect.id);
      held.file.effects.push(effect);
    }
  }

  for (const file of out) {
    file.effects.sort((a, b) => {
      const ordinal = b.ordinal - a.ordinal;
      return ordinal !== 0 ? ordinal : a.id.localeCompare(b.id);
    });
  }
  return out;
}
