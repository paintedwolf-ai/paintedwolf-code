import { SEARCH_RESULT_TYPES, type SearchResultTypeId } from "./search-result-types.ts";

/** Tabs shared by Crossbar and its shortcut catalog. */
export type CrossbarMode = "everything" | "actions" | SearchResultTypeId;

export const CROSSBAR_MODES: readonly CrossbarMode[] = [
  "everything",
  "actions",
  ...SEARCH_RESULT_TYPES.map((type) => type.id),
];

export const CROSSBAR_MODE_LABEL: Record<CrossbarMode, string> = {
  everything: "Everything",
  actions: "Actions",
  ...(Object.fromEntries(
    SEARCH_RESULT_TYPES.map((type) => [type.id, type.label]),
  ) as Record<SearchResultTypeId, string>),
};

export type SearchModeJumpCommandId = `search.mode.${CrossbarMode}`;

type SearchModeJumpCommand = {
  mode: CrossbarMode;
  commandId: SearchModeJumpCommandId;
  defaultChord: string;
};

if (CROSSBAR_MODES.length > 9) {
  throw new Error("Search mode jump shortcuts support at most nine modes");
}

/** One mode → command → default-chord mapping for UI, registry, and keymap. */
export const SEARCH_MODE_JUMP_COMMANDS: readonly SearchModeJumpCommand[] =
  CROSSBAR_MODES.map((mode, index) => ({
    mode,
    commandId: `search.mode.${mode}`,
    defaultChord: `Mod+${index + 1}`,
  }));

export const SEARCH_MODE_COMMAND_BY_MODE: Record<
  CrossbarMode,
  SearchModeJumpCommandId
> = Object.fromEntries(
  SEARCH_MODE_JUMP_COMMANDS.map(({ mode, commandId }) => [mode, commandId]),
) as Record<CrossbarMode, SearchModeJumpCommandId>;
