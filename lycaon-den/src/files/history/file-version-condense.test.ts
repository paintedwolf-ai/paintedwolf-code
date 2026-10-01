// @vitest-environment jsdom
import { EditorView } from "@codemirror/view";
import { afterEach, expect, it, vi } from "vitest";
import { Chunk } from "@codemirror/merge";
import { createSourceEditorState } from "../../components/source/editor/codemirror-theme.ts";
import { applyEditorScopeDiff } from "../../components/source/diff/scope-diff.ts";
import { condensedVersionLineRanges, foldCondensedContext } from "./file-version-condense.ts";

const views: EditorView[]=[];
afterEach(()=>{views.splice(0).forEach(view=>view.destroy());vi.restoreAllMocks();});
function editor(before:string,after:string){const view=new EditorView({state:createSourceEditorState({ surface: "files",doc:after,editable:true}),parent:document.body});views.push(view);applyEditorScopeDiff(view,{original:before});return view;}
it("folds context using the existing comparison without running another diff",()=>{
 const before=Array.from({length:80},(_,i)=>`line ${i}`).join('\n')+'\n';
 const view=editor(before,before.replace('line 40\n','changed\n'));
 const build=vi.spyOn(Chunk,'build');
 expect(condensedVersionLineRanges(view.state)).toEqual([{fromLine:0,toLine:37},{fromLine:44,toLine:81}]);
 expect(foldCondensedContext(view)).toBe(true);
 expect(build).not.toHaveBeenCalled();
});
it("does not fold identical files or short unchanged runs",()=>{
 expect(condensedVersionLineRanges(editor('same\n','same\n').state)).toEqual([]);
 const text=Array.from({length:9},(_,i)=>`line ${i}`).join('\n')+'\n';
 expect(condensedVersionLineRanges(editor(text,text.replace('line 4\n','changed\n')).state)).toEqual([]);
});
