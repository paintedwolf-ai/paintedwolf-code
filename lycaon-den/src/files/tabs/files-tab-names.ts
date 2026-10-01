import type { FileBuffer } from "../documents/files-buffer-state.ts";

type FileIdentity = Pick<FileBuffer, "key" | "name" | "path" | "rootId" | "rootLabel" | "jobId">;
export type FileTabName = { name: string; qualifier: string; label: string; path: string };

/** Duplicate basenames carry the shortest parent suffix that distinguishes them. */
export function filesTabNames(buffers: readonly FileIdentity[]): Map<string, FileTabName> {
  const groups = new Map<string, FileIdentity[]>();
  for (const buffer of buffers) {
    const group = groups.get(buffer.name) ?? [];
    group.push(buffer);
    groups.set(buffer.name, group);
  }
  const names = new Map<string, FileTabName>();
  for (const group of groups.values()) {
    const parents = group.map(buffer => [buffer.rootLabel || buffer.rootId, ...buffer.path.split("/").slice(0, -1)]);
    for (const [index, buffer] of group.entries()) {
      let qualifier = "";
      // A composed page has no address to distinguish it by; its name carries its identity.
      if (group.length > 1 && buffer.path) {
        const parent = parents[index]!;
        for (let depth = 1; depth <= parent.length; depth++) {
          qualifier = parent.slice(-depth).join("/");
          if (parents.every((peer, at) => at === index || peer.slice(-depth).join("/") !== qualifier)) break;
        }
        if (parents.some((peer, at) => at !== index && peer.join("/") === parent.join("/"))) {
          const sameRoot = group.filter(peer => peer.rootId === buffer.rootId);
          qualifier += sameRoot.length > 1
            ? ` · ${buffer.jobId ? buffer.jobId.startsWith("trust:") ? "Trust" : `Worker ${buffer.jobId}` : "Working file"}`
            : ` · ${buffer.rootId}`;
        }
      }
      names.set(buffer.key, { name: buffer.name, qualifier,
        label: qualifier ? `${buffer.name} — ${qualifier}` : buffer.name,
        path: `${buffer.rootLabel || buffer.rootId}/${buffer.path}` });
    }
  }
  return names;
}
