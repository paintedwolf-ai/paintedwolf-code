


/** Suffixes that may appear in TSX without a dedicated rule when the base hook is styled. */
export const UNSTYLED_HOOK_SUFFIXES = [
  "-static",
  "-from",
  "-to",
  "-pending",
  "-matched",
  "-traced",
  "-summary",
] as const;

export const BTN_UTILITY_PREFIX = "btn-";

/** btn-* / den-field* recipe hooks that must only appear in primitive component files. */
export const PRIMITIVE_RECIPE_HOOKS = [
  "btn-primary",
  "btn-primary-compact",
  "btn-secondary",
  "btn-ghost",
  "btn-danger",
  "btn-link",
  "btn-icon",
  "btn-compact",
  "den-field",
  "den-field-hint",
  "den-field-error",
  "den-input",
  "den-select",
  "den-rules-input",
  "den-trigger-group",
  "den-trigger-group__segment",
] as const;

/** Component paths (relative to lycaon-den/src) allowed to reference PRIMITIVE_RECIPE_HOOKS. */
export const PRIMITIVE_RECIPE_FILES = [
  "components/primitives/DenButton.tsx",
  "components/primitives/DenField.tsx",
  "components/primitives/DenInput.tsx",
  "components/primitives/DenMenuTrigger.tsx",
  "components/primitives/DenNumberInput.tsx",
  "components/primitives/DenSelect.tsx",
  "components/primitives/DenTextarea.tsx",
  "components/primitives/DenTriggerGroup.tsx",
] as const;

/** BEM prefixes reserved outside component class hooks. */
export const FORBIDDEN_COMPONENT_BEM_PREFIXES = [
  "settings-editor__",
  "shell__",
  "chat__",
  "composer__",
  "workers-drawer__",
  "workflows-drawer__",
  "plan-drawer__",
  "tool-part__",
] as const;

/** Bare roots forbidden as top-level selectors in *-domain.css. */
export const FORBIDDEN_DOMAIN_SELECTOR_ROOTS = [
  "shell",
  "chat",
  "composer",
  "workers-drawer",
  "workflows-drawer",
  "plan-drawer",
  "tool-part",
] as const;

export type HookCoverageGap = {
  hook: string;
  file: string;
};
