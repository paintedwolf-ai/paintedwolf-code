import type { LycaonClient } from "../../api/client.ts";
import { sourceReaderAccess } from "../../api/source-reader.ts";
import type { FileEditFold } from "./file-edit-fold.ts";

export function fileEditReader(client: LycaonClient, projectId: string, sessionId: string, fold: FileEditFold, index: number | null = null) {
  const first = index == null ? fold.steps[0] : fold.steps[index];
  const last = index == null ? fold.steps[fold.steps.length - 1] : first;
  if (!first || !last) throw new Error("The file edit has no retained references.");
  return sourceReaderAccess(client, projectId, { kind: "chat", first: first.snapshot.reference, last: last.snapshot.reference, expected_before_sha256: first.snapshot.before_sha256, expected_after_sha256: last.snapshot.after_sha256 }, sessionId);
}
