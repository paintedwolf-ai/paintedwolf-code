import { ApprovalCard } from "../../src/components/checkpoint/ApprovalCard.tsx";
import { CheckpointDecisionChicklet } from "../../src/components/checkpoint/CheckpointDecisionChicklet.tsx";
import { toolApprovalFixture } from "../../src/chat/checkpoint/approval-test-fixtures.ts";
import "../../src/components/source/reader/source-reader.css";
import "../../src/fonts/font-faces.generated.css";
import { registerOpenFilesSurfaceSink, applyOpenFilesSurfaceRequest } from "../../src/platform/navigation/open-files-surface.ts";
import type { ChatContentDocument } from "../../src/chat/transcript/content/chat-content-document.ts";
import { ChatContentPage } from "../../src/files/components/ChatContentPage.tsx";
import { EditorCommandBar } from "../../src/find/EditorCommandBar.tsx";
import "../../src/tokens.generated.css";
import "../../src/tokens-derived.css";
import "../../src/tailwind.css";
import "../../src/global.css";
import "../../src/markdown.css";
import "../../src/platform/themed-scrollbars.css";
import { render } from "solid-js/web";
import { createSignal, onCleanup, Show } from "solid-js";
import type { ChatContentRow, Message } from "../../src/api/types.ts";
import type { LycaonClient } from "../../src/api/client.ts";
import { ActivitySpanCard } from "../../src/components/tool/ActivitySpanCard.tsx";
import { GenericToolCard } from "../../src/components/tool/GenericToolCard.tsx";
import { toolPartFromCall } from "../../src/chat/tool/tool-part-model.ts";
import { createTranscriptDisclosureStore, TranscriptDisclosureProvider } from "../../src/chat/transcript/presentation/disclosure-state.tsx";
import { setupThemedScrollbars } from "../../src/platform/scrolling/themed-scrollbars.ts";

const totalRows = 20000;
const body = (index: number) => `Line ${index}: ` + "A complete word stays together. ".repeat(index%5===0 ? 12 : 2) + "🌲\n";
const offsets = [0];
for (let i=0;i<totalRows;i++) offsets.push(offsets[i]!+Array.from(body(i)).length);
const row = (index: number): ChatContentRow => ({ index,offset:offsets[index]!,text:body(index),spans:[] });
function range(start: number, budget: number): ChatContentRow[] {
 const values: ChatContentRow[]=[];
 for (let index=start;index<Math.min(start+64,totalRows);index++) {
  const value=row(index), size=new TextEncoder().encode(value.text).length;
  if (values.length && size>budget) break;
  values.push(value); budget-=size;
 }
 return values;
}
const preview=range(0,8192);
const entries=Array.from({ length:10000 },(_,index)=>{
 const call={ id:`call-${index}`,name:"read",args:{ path:`document-${index}.md` },display_title:`document-${index}.md` };
 const result: Message={ id:`result-${index}`,role:"tool",origin:"tool",authority:"none",trust_tier:"untrusted",created_at:"2026-09-15T12:00:00Z",tool_result:{ content:"",tool:"read",tool_call_id:call.id,content_ref:{ field:"tool_output",tool_call_id:call.id,sha256:"fixture",rows:totalRows,total_runes:offsets.at(-1)!,size_bytes:offsets.at(-1)!+totalRows*3,preview_rows:preview } } };
 return { kind:"tool" as const,part:toolPartFromCall(call,result,`assistant-${index}`) };
});
function Fixture() {
 const [approvals,setApprovals]=createSignal(false);
 const [decisions,setDecisions]=createSignal(0);
 const [requests,setRequests]=createSignal(0);
 const [width,setWidth]=createSignal(640);
 const [document,setDocument]=createSignal<ChatContentDocument>();
 onCleanup(registerOpenFilesSurfaceSink(request => { if(request.kind === "chat-content") { applyOpenFilesSurfaceRequest(request,[]); setDocument(request.document); } }));
 const getRows: LycaonClient["getChatContent"]=async (_session,_message,reference,position,signal)=>{
  setRequests(value=>value+1);
  await new Promise(resolve=>setTimeout(resolve,30));
  if (signal?.aborted) throw new DOMException("Canceled","AbortError");
  if (!("row" in position) && !("locate" in position)) throw new Error("Unexpected byte range read");
  const start="row" in position ? position.row : Math.max(0,offsets.findIndex(offset=>offset>position.locate)-1);
  const rows=range(start,16384), next=rows.at(-1)!.index+1;
  return { reference,rows,offset:offsets[start]!,end_offset:offsets[next]!,complete:next===totalRows,text:"",spans:[] };
 };
 const search: LycaonClient["searchChatContent"]=async (_session,_message,_reference,query,cursor,sensitive)=>{
  const offset=Number(cursor ?? 0);
  const needle=sensitive ? query : query.toLowerCase();
  const matches=Array.from({length:totalRows},(_,index)=>{
   const text=body(index), at=(sensitive ? text : text.toLowerCase()).indexOf(needle);
   return at<0 ? undefined : {offset:offsets[index]!+Array.from(text.slice(0,at)).length,length:Array.from(query).length};
  }).filter((match):match is {offset:number;length:number}=>!!match && match.offset>=offset);
  const page=matches.slice(0,100);
  return page.length===matches.length ? {matches:page} : {matches:page,next_cursor:String(page.at(-1)!.offset+1)};
 };
 const client = new Proxy({ getChatContent:getRows,searchChatContent:search },{ get(target,key){ if(key in target) return target[key as keyof typeof target]; if(key==="then") return undefined; return ()=>Promise.reject(new Error(`Unexpected request: ${String(key)}`)); } }) as LycaonClient;
 onCleanup(setupThemedScrollbars());
 return <TranscriptDisclosureProvider value={createTranscriptDisclosureStore()}>
  <main data-testid="fixture-scroll" style={{ "font-family":"var(--den-font-ui)",padding:"24px",width:`${width()}px`,margin:"0 auto",height:"100vh",overflow:"auto" }}>
   <EditorCommandBar />
   <h1>Chat content scroll fixture</h1>
   <p>20,000 lines in each of 10,000 tool cards.</p>
   <button type="button" onClick={()=>setWidth(400)}>Narrow</button>{" "}<button type="button" onClick={()=>setWidth(640)}>Wide</button>
   <button type="button" onClick={()=>setApprovals(value=>!value)}>Show {approvals() ? "tools" : "approvals"}</button>
   <p>Content requests: <output data-testid="content-request-count">{requests()}</output></p>
   <Show when={approvals()} fallback={<ActivitySpanCard label="Read files" entries={entries} layout="chat" sessionId="fixture" entryKey="fixture-actions" renderToolEntry={part=><GenericToolCard part={part()} layout="chat" sessionId="fixture" projectId="fixture" client={client} />} />}>
    <output data-testid="approval-decision-count">{decisions()}</output>
    <ApprovalCard projectId="fixture" sessionId="fixture" checkpoint={{checkpointId:"approval-fixture",sessionId:"fixture",kind:"tool_approval",status:"pending",issuedAt:new Date().toISOString(),
      tool_approval:toolApprovalFixture({subject:{kind:"destination_set",title:"Allow network: 1000 configured endpoints",targets:Array.from({length:1000},(_,index)=>({kind:"destination",label:`host-${index}.example`}))},
        presentation:{who:"Current worker",if_wrong:"Unintended destinations could receive requests."}})}} onToolApproval={()=>setDecisions(value=>value+1)} onContentApply={()=>setDecisions(value=>value+1)} />
    <CheckpointDecisionChicklet projectId="fixture" sessionId="fixture" entryKey="approval-fixture-decision" meta={{checkpoint_id:"approval-recorded",kind:"tool_approval",status:"rejected",tool:"command",subject:"Remove generated files",guidance:"Preserve the build manifest and inspect the generated directory first."}} />
   </Show>
   <Show when={document()}>{value => <div style={{ height: "500px", display: "flex", "flex-direction": "column" }}><ChatContentPage projectId="fixture" document={value()} client={client} /></div>}</Show>
  </main>
 </TranscriptDisclosureProvider>;
}
render(()=><Fixture />,document.getElementById("fixture")!);
