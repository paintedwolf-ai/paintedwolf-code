/** Scope marks shared by the Files tree and open list. */

export type ScopeChangeKind = "added" | "changed" | "deleted";

/** A mark reports the scope in view, or the change a shown version made. */
export type ChangeMarkSubject = "view" | "version";

export type FileChange = { kind: ScopeChangeKind; subject: ChangeMarkSubject };

export type ScopeChangeMarkSpec = {
  kind: ScopeChangeKind;
  glyph: string;
  /** Spoken / tooltip copy. */
  label: string;
};

const MARKS: Record<ScopeChangeKind, { glyph: string; verb: string }> = {
  added: { glyph: "+", verb: "Added" },
  changed: { glyph: "•", verb: "Changed" },
  deleted: { glyph: "−", verb: "Deleted" },
};

export function scopeChangeMarkSpec(change: FileChange): ScopeChangeMarkSpec {
  const mark = MARKS[change.kind];
  return {
    kind: change.kind,
    glyph: mark.glyph,
    label: `${mark.verb} ${change.subject === "version" ? "in this version" : "in the current view"}`,
  };
}

export function scopeChangeKindFromFlags(args: {
  inScope: boolean;
  added: boolean;
  deleted: boolean;
}): ScopeChangeKind | null {
  if (args.deleted) return "deleted";
  if (args.added) return "added";
  if (!args.inScope) return null;
  return "changed";
}
