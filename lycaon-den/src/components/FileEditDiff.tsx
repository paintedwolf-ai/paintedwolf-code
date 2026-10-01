import {
  fileEditChange,
  fileEditFoldIsNoop,
  fileEditLineStat,
} from "../chat/file-edit/file-edit-fold.ts";
import type {
  FileEditFold,
  FileEditLineStat,
} from "../chat/file-edit/file-edit-fold.ts";
import { fileEditReader } from "../chat/file-edit/file-edit-reader.ts";
import { getLycaonClient } from "../platform/connection/app-connection.ts";
import type { DiffRowSource } from "./source/diff/diff-row-source.ts";
import { SourceDiffRow } from "./source/diff/SourceDiffRow.tsx";
import type { TranscriptDisclosureKey } from "../chat/transcript/presentation/transcript-disclosure-key.ts";

type Props = {
  fold: FileEditFold;
  onNetStat?: (stat: FileEditLineStat) => void;
  projectId?: string;
  sessionId?: string | null;
  /** Worker overlay; null selects the project tree. */
  jobId?: string | null;
  /** Project roots used by the path menu's Add to chat action. */
  rootRefs?: readonly import("../api/project-path.ts").ResolveProjectRoot[];
  chrome?: "card" | "row";
  /** Transcript identity of this file's disclosure. */
  disclosureKey?: TranscriptDisclosureKey;
};

/** The writes one reply recorded, composed into the file's net change. */
export function FileEditDiff(props: Props) {
  const steps = () => props.fold.steps;
  const shown = (revision: number | null) =>
    (revision === null ? null : steps()[revision]?.snapshot) ?? props.fold.net;
  const source: DiffRowSource = {
    get key() { return props.fold.key; },
    get projectId() { return props.projectId; },
    get sessionId() { return props.sessionId; },
    get jobId() { return props.jobId; },
    get rootRefs() { return props.rootRefs; },
    get revisions() {
      const held = steps();
      return held.map((step, index) => ({
        key: step.key,
        label: String(index + 1),
        ariaLabel: `Edit ${index + 1} of ${held.length}, ${step.tool}`,
      }));
    },
    get netLabel() { return "Net"; },
    get revisionsLabel() { return `${steps().length} writes to ${props.fold.path}`; },
    get noopNote() {
      return `${steps().length} writes, and the file ends as it started. ` +
        "Open a revision to see what changed in between.";
    },
    path: (revision) => shown(revision).path,
    rootId: (revision) => shown(revision).root_id,
    openable: (revision) => (shown(revision).deleted ? false : undefined),
    change: (revision) => fileEditChange(shown(revision)),
    stat: (revision) => fileEditLineStat(shown(revision)),
    isNoop: (revision) => revision === null && fileEditFoldIsNoop(props.fold),
    access: (revision) => {
      const client = getLycaonClient();
      if (!client || !props.projectId || !props.sessionId) return undefined;
      return fileEditReader(client, props.projectId, props.sessionId, props.fold, revision);
    },
  };

  return (
    <SourceDiffRow
      source={source}
      onNetStat={props.onNetStat}
      chrome={props.chrome}
      disclosureKey={props.disclosureKey}
    />
  );
}
