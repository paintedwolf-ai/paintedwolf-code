/** Nav / registry icons for Context stages — registered with the stage registry. */
import type { JSX } from "solid-js";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { SearchIcon } from "../search/SearchIcons.tsx";
import { SecurityScannersIcon } from "../settings/SettingsEditorTitle.tsx";

export function StageSearchIcon(): JSX.Element {
  return <SearchIcon />;
}

export function StageFilesIcon(): JSX.Element {
  return <ThemeIcon slot="stage-files" size={14} />;
}

export function StageSecurityIcon(): JSX.Element {
  return <SecurityScannersIcon size={14} />;
}

export function StageCostIcon(): JSX.Element {
  return <ThemeIcon slot="stage-cost" size={14} />;
}

export function StageArtifactsIcon(): JSX.Element {
  return <ThemeIcon slot="stage-artifacts" size={14} />;
}

export function StageBlueprintsIcon(): JSX.Element {
  return <ThemeIcon slot="stage-blueprints" size={14} />;
}

export function StageExtensionsIcon(): JSX.Element {
  return <ThemeIcon slot="stage-extensions" size={14} />;
}
