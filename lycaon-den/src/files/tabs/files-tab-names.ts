import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { bufferCheckoutQualifier } from "../source/files-checkout-scope.ts";
import { outsideHostPath } from "../source/outside-namespace.ts";

type FileIdentity = Pick<FileBuffer, "key" | "name" | "path" | "rootId" | "rootLabel" | "jobId"> &
  Partial<Pick<FileBuffer, "branchId" | "worktreeId" | "checkoutLabel" | "checkoutUnavailable">>;
export type FileTabName = { name: string; qualifier: string; label: string; path: string };

/**
 * Duplicate basenames carry the shortest parent suffix that distinguishes them.
 * An outside file is named by its host path rather than a root label.
 */
export function filesTabNames(buffers: readonly FileIdentity[], isOutside: (rootId: string) => boolean = () => false): Map<string, FileTabName> {
  const groups = new Map<string, FileIdentity[]>();
  for (const buffer of buffers) {
    const group = groups.get(buffer.name) ?? [];
    group.push(buffer);
    groups.set(buffer.name, group);
  }
  const names = new Map<string, FileTabName>();
  for (const group of groups.values()) {
    const parents = group.map(buffer => isOutside(buffer.rootId)
      ? ["", ...buffer.path.split("/").slice(0, -1)]
      : [buffer.rootLabel || buffer.rootId, ...buffer.path.split("/").slice(0, -1)]);
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
          const distinctCheckouts = sameRoot.some(peer => !peer.jobId && !buffer.jobId && (peer.branchId ?? "") !== (buffer.branchId ?? ""));
          const checkoutLabel = distinctCheckouts ? bufferCheckoutQualifier(buffer) : "";
          qualifier += sameRoot.length > 1
            ? ` · ${buffer.jobId ? buffer.jobId.startsWith("trust:") ? "Trust" : `Worker ${buffer.jobId}` : checkoutLabel || "Working file"}`
            : ` · ${buffer.rootId}`;
        }
      } else if (buffer.checkoutUnavailable) {
        qualifier = bufferCheckoutQualifier(buffer);
      }
      names.set(buffer.key, { name: buffer.name, qualifier,
        label: qualifier ? `${buffer.name} — ${qualifier}` : buffer.name,
        path: isOutside(buffer.rootId) ? outsideHostPath(buffer.path) : `${buffer.rootLabel || buffer.rootId}/${buffer.path}` });
    }
  }
  return names;
}
