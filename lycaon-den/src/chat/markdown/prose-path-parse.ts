export type ProsePathTarget = {
  path: string;
  reference?: string;
  jobId?: string;
  line?: number;
  endLine?: number;
  projectId?: string;
  rootId?: string;
  entryKind?: "file" | "folder";
};

/** Parse a path mention with an optional line range. */
export function parseProsePathCandidate(candidate: string): ProsePathTarget | undefined {
  let path = candidate.trim().replace(/\\/g, "/");
  if (!path || /[\r\n\x00]/.test(path)) return undefined;

  let line: number | undefined;
  let endLine: number | undefined;

  const hashIndex = path.lastIndexOf("#");
  if (hashIndex !== -1) {
    const fragment = path.slice(hashIndex + 1);
    path = path.slice(0, hashIndex);
    const fragMatch = fragment.match(/^L?(\d+)(?:-L?(\d+))?$/i);
    if (fragMatch) {
      const n = Number(fragMatch[1]);
      if (Number.isFinite(n) && n > 0) line = n;
      if (fragMatch[2]) {
        const e = Number(fragMatch[2]);
        if (Number.isFinite(e) && e > 0) endLine = e;
      }
    }
  }

  const lineMatch = path.match(/:(\d+)(?:-(\d+))?$/);
  if (lineMatch) {
    const n = Number(lineMatch[1]);
    if (Number.isFinite(n) && n > 0) line = n;
    if (lineMatch[2]) {
      const e = Number(lineMatch[2]);
      if (Number.isFinite(e) && e > 0) endLine = e;
    }
    path = path.slice(0, -lineMatch[0].length);
  }

  while (path.startsWith("./")) path = path.slice(2);
  if (!path) return undefined;
  if (line != null && endLine != null && endLine < line) {
    const tmp = line;
    line = endLine;
    endLine = tmp;
  }
  return { path, line, endLine };
}
