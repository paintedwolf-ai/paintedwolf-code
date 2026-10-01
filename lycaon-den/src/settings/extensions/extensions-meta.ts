import type {
  ExtensionMetaPackStatus,
  ExtensionMetaPackSummary,
  ExtensionPackSummary,
} from "../../api/types.ts";
import type { ExtensionChip } from "./extensions-chips.ts";
import { EXTENSIONS_SETTINGS_COPY as C } from "./extensions-settings-copy.ts";

const STOCK_META_ID = "painted-wolf/stock";

export function metaPackIdSafe(id: string): string {
  return id.split("/").join("-");
}

export type MetaPackSection = {
  meta: ExtensionMetaPackSummary;
  packs: ExtensionPackSummary[];
};

export type MetaPackGrouping = {
  sections: MetaPackSection[];
  other: ExtensionPackSummary[];
};

export function groupPacksByMeta(
  packs: readonly ExtensionPackSummary[],
  metas: readonly ExtensionMetaPackSummary[],
): MetaPackGrouping {
  const sortedMetas = [...metas].sort((a, b) => {
    if (a.id === STOCK_META_ID) return -1;
    if (b.id === STOCK_META_ID) return 1;
    return a.id.localeCompare(b.id);
  });
  const memberOf = new Set<string>();
  for (const m of sortedMetas) {
    for (const id of m.members) memberOf.add(id);
  }
  const byId = new Map(packs.map((p) => [p.id, p]));
  const sections: MetaPackSection[] = sortedMetas.map((meta) => ({
    meta,
    packs: meta.members
      .map((id) => byId.get(id))
      .filter((p): p is ExtensionPackSummary => p != null),
  }));
  const other = packs.filter((p) => !memberOf.has(p.id));
  return { sections, other };
}

export function metaPackStatusChip(
  status: ExtensionMetaPackStatus,
): ExtensionChip {
  switch (status) {
    case "complete":
      return { label: C.statusComplete, className: "stock" };
    case "partial":
      return { label: C.statusPartial, className: "conflict" };
    case "inactive":
      return { label: C.statusInactive, className: "disabled" };
    default:
      return { label: status, className: "feature" };
  }
}

export function metaPackStatusChips(
  meta: ExtensionMetaPackSummary,
): ExtensionChip[] {
  const chips: ExtensionChip[] = [];
  const codes = new Set(meta.diagnostics.map((d) => d.code));
  if (codes.has("suite_conflict")) {
    chips.push({ label: C.chipSuiteConflict, className: "conflict" });
  }
  if (meta.status === "partial" || codes.has("member_disabled")) {
    chips.push({ label: C.chipPartialSuite, className: "conflict" });
  }
  if (codes.has("member_missing")) {
    chips.push({ label: C.chipMissingMember, className: "conflict" });
  }
  return chips;
}
