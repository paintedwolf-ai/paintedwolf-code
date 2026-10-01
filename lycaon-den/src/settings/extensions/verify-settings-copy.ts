export const VERIFY_SETTINGS_COPY = {
  editorHint:
    "The default project check. The agent chooses inspection, targeted checks, or this command according to the change and your instructions. Selecting it does not run the full suite after every edit. Required workflow test gates still need a pass; blocked checks are reported separately from completed work.",
  nudgeConsequence:
    "This becomes the default project check, not a requirement to run the full suite after every edit. The agent reports the validation appropriate to each change and any blockers.",
} as const;
