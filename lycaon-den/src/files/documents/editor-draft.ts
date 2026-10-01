import type { SourceDeletedFile, SourceEncoding } from "../../api/types.ts";
import type { EolKind } from "../../components/source/editor/eol.ts";

/** The local edit facts needed to publish and acknowledge a host document. */
export type EditorDraft = {
  key: string;
  rootId: string;
  fileId: string | null;
  path: string;
  documentId: string | null;
  documentRevision: number | null;
  kind: string;
  jobId?: string;
  deleted?: SourceDeletedFile;
  encoding: SourceEncoding | null;
  eol: EolKind;
  mixedEol: boolean;
  editRevision: number;
};

export type EditorDraftSource = {
  get: (key: string) => EditorDraft | undefined;
  all: () => readonly EditorDraft[];
};

export type EditorEditingBlock = "storage" | "capacity";
