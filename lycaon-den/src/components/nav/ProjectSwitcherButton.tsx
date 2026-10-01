import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import type { ProjectSummary } from "../../project/project-summary.ts";

type Props = {
  project: ProjectSummary;
  onOpenLauncher: () => void;
};

export function ProjectSwitcherButton(props: Props) {
  return (
    <button
      type="button"
      class="project-switcher"
      data-testid="project-switcher"
      aria-label={`Switch project — ${props.project.displayName}`}
      onClick={() => props.onOpenLauncher()}
    >
      <span class="project-switcher__glyph" aria-hidden="true">
        <ThemeIcon slot="project-switcher" size={14} />
      </span>
      <span class="project-switcher__name">{props.project.displayName}</span>
    </button>
  );
}
