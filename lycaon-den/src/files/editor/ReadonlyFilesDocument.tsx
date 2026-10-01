import type { JSX } from "solid-js";
import { bindLayoutBand, LAYOUT_BAND_SCALES } from "../../layout/layout-bands.ts";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { CopyIcon, FindIcon, GotoLineIcon } from "../../components/source/viewer-icons.tsx";
import { FilesEditorToolbar, FilesEditorStatus, type FilesEditorStatusProps } from "./FilesEditorChrome.tsx";
import { FilesPathCrumbs } from "./FilesPathCrumbs.tsx";

export function ReadonlyFilesDocument(props: {
  title: string;
  onFind: () => void;
  onGotoLine: () => void;
  onCopy: () => void;
  status: FilesEditorStatusProps;
  children: JSX.Element;
}) {
  return <div class="den-files-editor" ref={element => bindLayoutBand(element, LAYOUT_BAND_SCALES.filesEditor)}>
    <FilesEditorToolbar actions={<>
        <button type="button" class="den-files-editor__tool den-inset-icon-btn" aria-label="Find in file" data-tip={`Find (${bindingForHandler("find.inView")})`} data-tip-pos="below" onClick={props.onFind}><FindIcon /></button>
        <button type="button" class="den-files-editor__tool den-inset-icon-btn" aria-label="Go to line" data-tip={`Go to line (${bindingForHandler("editor.goToLine")})`} data-tip-pos="below" onClick={props.onGotoLine}><GotoLineIcon /></button>
        <button type="button" class="den-files-editor__tool den-inset-icon-btn" aria-label="Copy contents" data-tip="Copy contents" data-tip-pos="below" onClick={props.onCopy}><CopyIcon /></button>
    </>}>
      <FilesPathCrumbs rootId="" rootLabel="" path="" onRevealSegment={() => {}}>
        <span class="den-files-editor__crumb-seg den-files-editor__crumb-file">{props.title}</span>
      </FilesPathCrumbs>
    </FilesEditorToolbar>
    <div class="den-files-editor__document-stage">{props.children}</div>
    <FilesEditorStatus {...props.status} />
  </div>;
}
