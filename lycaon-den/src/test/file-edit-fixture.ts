import { diffLines } from "diff";
import type { FileEditPreview, FileEditSnapshot } from "../api/types.ts";
import { sha256Hex } from "./sha256.ts";

export const sourceTextHash = sha256Hex;

export const retainedFileEditFixtures = new Map<string, FileEditSnapshot>();

/** Creates a transport fixture from readable test source, retaining the bodies on the mock host. */
export function fileEditPreviewFixture(edit: FileEditSnapshot): FileEditPreview {
  const hash = sha256Hex;
  const id = hash(JSON.stringify(edit));
  retainedFileEditFixtures.set(id, edit);
  const changes = diffLines(edit.before ?? "", edit.after);
  return { path: edit.path, root_id: edit.root_id, reference: { message_id: id, tool_call_id: id, index: 0 },
    before_sha256: hash(edit.before ?? ""), after_sha256: hash(edit.after), created: edit.before == null, deleted: !!edit.deleted,
    added: changes.filter(change => change.added).reduce((sum,change) => sum + (change.count ?? 0),0),
    removed: changes.filter(change => change.removed).reduce((sum,change) => sum + (change.count ?? 0),0),
  };
}
