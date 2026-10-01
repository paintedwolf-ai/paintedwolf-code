import {
  scopeChangeMarkSpec,
  type FileChange,
} from "./files-scope-change-mark.ts";

export type FilesScopeChangeMarkSurface = "tree" | "open-list";

type Props = {
  change: FileChange;
  surface: FilesScopeChangeMarkSurface;
};

function testIdFor(surface: FilesScopeChangeMarkSurface): string {
  if (surface === "tree") return "files-tree-scope-mark";
  return "files-open-list-scope-mark";
}

export function FilesScopeChangeMark(props: Props) {
  const spec = () => scopeChangeMarkSpec(props.change);
  const markClassList = () => ({
    "den-files-scope-mark--added": props.change.kind === "added",
    "den-files-scope-mark--changed": props.change.kind === "changed",
    "den-files-scope-mark--deleted": props.change.kind === "deleted",
    "den-files-scope-mark--tree": props.surface === "tree",
    "den-files-scope-mark--open-list": props.surface === "open-list",
  });

  return (
    <span
      class="den-files-scope-mark"
      classList={markClassList()}
      data-testid={testIdFor(props.surface)}
      data-kind={props.change.kind}
      data-tip={props.surface === "open-list" ? undefined : spec().label}
      aria-label={spec().label}
    >
      <span aria-hidden="true">{spec().glyph}</span>
    </span>
  );
}
