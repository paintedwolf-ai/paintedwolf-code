import { DenButton } from "../primitives/DenButton.tsx";
import { OpenInButton } from "../OpenInButton.tsx";
import type { LocalPathTarget } from "../../platform/navigation/open-local-path.ts";
import type { ProjectRoot } from "../../api/types.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";

type Props = {
  root: ProjectRoot;
  path: string;
  childCount: number | null;
  openIn: LocalPathTarget;
  onCopyPath: () => void;
  onCopyRelativePath: () => void;
};

function folderName(path: string): string {
  const segments = path.split("/").filter(Boolean);
  return segments[segments.length - 1] ?? path;
}

function itemCountLabel(count: number | null): string {
  if (count == null) return "Loading contents…";
  return `${new Intl.NumberFormat().format(count)} ${count === 1 ? "item" : "items"}`;
}

export function ProjectFolderSummary(props: Props) {
  const rootLabel = () => props.root.label;
  const pathReference = () => `@${rootLabel()}/${props.path}`;

  return (
    <section
      class="den-files-folder-summary"
      data-testid="files-folder-summary"
      aria-labelledby="files-folder-summary-title"
    >
      <div class="den-files-folder-summary__header flex items-start gap-2.5">
        <span class="den-files-folder-summary__mark">
          <ThemeIcon slot="project-folder" size={20} />
        </span>
        <div class="min-w-0 flex-1">
          <div class="den-files-folder-summary__title-row flex flex-wrap items-center gap-1.5">
            <h2 id="files-folder-summary-title">
              {folderName(props.path)}
            </h2>
            <span class="den-status-mark">Folder</span>
          </div>
          <p
            class="den-files-folder-summary__path"
            data-testid="files-folder-summary-path"
            data-tip={pathReference()}
            data-tip-when-clipped
          >
            {pathReference()}
          </p>
          <p
            class="den-files-folder-summary__count"
            data-testid="files-folder-summary-count"
          >
            {itemCountLabel(props.childCount)}
          </p>
        </div>
        <div class="den-files-folder-summary__actions box-border flex shrink-0 flex-wrap gap-1">
          <DenButton
            variant="ghost"
            compact
            data-testid="files-folder-summary-copy-path"
            onClick={props.onCopyPath}
          >
            Copy path
          </DenButton>
          <DenButton
            variant="ghost"
            compact
            data-testid="files-folder-summary-copy-relative-path"
            onClick={props.onCopyRelativePath}
          >
            Copy relative path
          </DenButton>
          <OpenInButton compact target={props.openIn} />
        </div>
      </div>
    </section>
  );
}
