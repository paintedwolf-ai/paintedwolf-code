import type {
  SearchReplaceApplyFile,
  SearchReplaceFilePreview,
} from "../api/types.ts";

export type ReplaceFileSelection = {
  rootId: string;
  path: string;
  sha256: string;
  checked: boolean;
  collapsed: boolean;
  hunks: boolean[];
};

export type ReplaceSelection = {
  files: ReplaceFileSelection[];
};

/** All files and hunks checked by default; groups start expanded. */
export function initReplaceSelection(
  files: readonly SearchReplaceFilePreview[],
): ReplaceSelection {
  return {
    files: files.map((f) => ({
      rootId: f.root_id,
      path: f.path,
      sha256: f.sha256,
      checked: true,
      collapsed: false,
      hunks: f.hunks.map(() => true),
    })),
  };
}

/** File and hunk content preserve selections when line numbers shift. */
export function reconcileReplaceSelection(
  previous: ReplaceSelection | null,
  previousFiles: readonly SearchReplaceFilePreview[],
  files: readonly SearchReplaceFilePreview[],
): ReplaceSelection {
  if (!previous) return initReplaceSelection(files);
  const fileKey = (rootId: string, path: string) =>
    JSON.stringify([rootId, path]);
  // Occurrence ordinals distinguish identical hunk bodies.
  const hunkKeys = (hunks: readonly { before: string; after: string }[]) => {
    const counts = new Map<string, number>();
    return hunks.map((h) => {
      const body = JSON.stringify([h.before, h.after]);
      const ordinal = counts.get(body) ?? 0;
      counts.set(body, ordinal + 1);
      return `${body}#${ordinal}`;
    });
  };
  const priorByFile = new Map<
    string,
    { selection: ReplaceFileSelection; hunkChecked: Map<string, boolean> }
  >();
  previous.files.forEach((selection, index) => {
    const preview = previousFiles[index];
    if (!preview) return;
    const hunkChecked = new Map<string, boolean>();
    hunkKeys(preview.hunks).forEach((key, hi) => {
      hunkChecked.set(key, selection.hunks[hi] ?? true);
    });
    priorByFile.set(fileKey(selection.rootId, selection.path), {
      selection,
      hunkChecked,
    });
  });
  return {
    files: files.map((f) => {
      const prior = priorByFile.get(fileKey(f.root_id, f.path));
      const hunks = hunkKeys(f.hunks).map(
        (key) => prior?.hunkChecked.get(key) ?? true,
      );
      return {
        rootId: f.root_id,
        path: f.path,
        sha256: f.sha256,
        checked: hunks.some(Boolean),
        collapsed: prior?.selection.collapsed ?? false,
        hunks,
      };
    }),
  };
}

export function toggleReplaceFile(
  selection: ReplaceSelection,
  index: number,
  checked: boolean,
): ReplaceSelection {
  return {
    files: selection.files.map((f, i) => {
      if (i !== index) return f;
      return {
        ...f,
        checked,
        hunks: f.hunks.map(() => checked),
      };
    }),
  };
}

export function toggleReplaceHunk(
  selection: ReplaceSelection,
  fileIndex: number,
  hunkIndex: number,
  checked: boolean,
): ReplaceSelection {
  return {
    files: selection.files.map((f, i) => {
      if (i !== fileIndex) return f;
      const hunks = f.hunks.map((h, hi) => (hi === hunkIndex ? checked : h));
      const any = hunks.some(Boolean);
      return { ...f, hunks, checked: any };
    }),
  };
}

export function setReplaceFileCollapsed(
  selection: ReplaceSelection,
  index: number,
  collapsed: boolean,
): ReplaceSelection {
  return {
    files: selection.files.map((f, i) =>
      i === index ? { ...f, collapsed } : f,
    ),
  };
}

export function replaceSelectionCounts(selection: ReplaceSelection): {
  matches: number;
  files: number;
} {
  let matches = 0;
  let files = 0;
  for (const f of selection.files) {
    const n = f.hunks.filter(Boolean).length;
    if (n > 0) {
      files += 1;
      matches += n;
    }
  }
  return { matches, files };
}

export function selectionToApplyFiles(
  selection: ReplaceSelection,
): SearchReplaceApplyFile[] {
  const out: SearchReplaceApplyFile[] = [];
  for (const f of selection.files) {
    const hunks: number[] = [];
    f.hunks.forEach((on, i) => {
      if (on) hunks.push(i);
    });
    if (hunks.length === 0) continue;
    out.push({
      root_id: f.rootId,
      path: f.path,
      sha256: f.sha256,
      hunks,
    });
  }
  return out;
}

/** Count previewed hunks whose syntactic context matches. */
export function countHunksByContext(
  files: readonly SearchReplaceFilePreview[],
  context: "code" | "comment_or_string",
): number {
  let n = 0;
  for (const f of files) {
    for (const h of f.hunks) {
      if (h.context === context) n++;
    }
  }
  return n;
}

/** Check/uncheck every hunk of one syntactic context class across all files. */
export function setHunksCheckedByContext(
  selection: ReplaceSelection,
  files: readonly SearchReplaceFilePreview[],
  context: "code" | "comment_or_string",
  checked: boolean,
): ReplaceSelection {
  return {
    files: selection.files.map((f, fi) => {
      const preview = files[fi];
      if (!preview) return f;
      const hunks = f.hunks.map((on, hi) =>
        preview.hunks[hi]?.context === context ? checked : on,
      );
      return { ...f, hunks, checked: hunks.some(Boolean) };
    }),
  };
}

export function skipReasonLabel(reason: string | undefined): string {
  switch (reason) {
    case "source_write_conflict":
      return "Changed since preview";
    case "source_too_large":
      return "File too large";
    case "source_binary":
      return "Binary file";
    case "source_not_found":
      return "File not found";
    default:
      return reason?.trim() ? reason : "Skipped";
  }
}
