import { writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { apiConfig, modelIndependentWebE2e as test, openProjectFilesFixture } from "./helpers.ts";

declare global {
  interface Window {
    historyProbe: { editors: Set<Element>; bars: Set<Element>; slow: unknown[]; reads: number; ms: number };
  }
}

test("reuses historical viewports across file-version navigation", async ({ page, request }, info) => {
  info.setTimeout(180_000);
  const names = ["a.ts", "b.ts", "c.ts"];
  const body = (name: string, version: string) => `// ${version} ${name}\n` + Array.from({ length: 400 }, (_, i) => `export const value${i} = ${i};`).join("\n");
  const { project } = await openProjectFilesFixture(page, request, { prefix: "history-investigation", name: "History investigation", seed: root => {
    for (const name of [...names,"unrelated.ts"]) writeFileSync(path.join(root,name),body(name,"before"));
  }});
  const { apiUrl,token } = apiConfig(); const headers = { Authorization: `Bearer ${token}` }; const base = `${apiUrl}/v1/projects/${project.id}/source`; const rootId = project.roots[0]!.id;
  await expect.poll(async () => (await (await request.get(`${base}/workspace`,{headers})).json()).inventory.complete).toBe(true);
  const pin = await request.post(`${base}/pins`,{headers,data:{label:"Before edits"}}); expect(pin.ok()).toBe(true); const {id:pinId}=await pin.json();
  for (const name of names) {
    const source = await (await request.get(base,{headers,params:{root_id:rootId,path:name}})).json();
    const saved=await request.put(base,{headers,data:{operation_id:crypto.randomUUID(),root_id:rootId,path:name,base_sha256:source.sha256,encoding:source.encoding,content:body(name,"after")}});expect(saved.ok(),await saved.text()).toBe(true);
  }
  const session=await request.post(`${apiUrl}/v1/sessions`,{headers,data:{project_id:project.id,posture:"build"}});expect(session.ok()).toBe(true);const {id:sessionId}=await session.json();
  await page.evaluate(() => {
    const p={editors:new Set<Element>(),bars:new Set<Element>(),slow:[] as unknown[],reads:0,ms:0};Object.assign(window,{historyProbe:p});
    const collect=(el:Element)=>{ for(const [sel,set] of [[".cm-editor",p.editors],[".cm-editor .os-scrollbar-vertical",p.bars]] as const){if(el.matches(sel))set.add(el);el.querySelectorAll(sel).forEach(e=>set.add(e));}};
    collect(document.body);new MutationObserver(rs=>rs.forEach(r=>r.addedNodes.forEach(n=>{if(n instanceof Element)collect(n);}))).observe(document.body,{subtree:true,childList:true});
    const rect=Element.prototype.getBoundingClientRect;Element.prototype.getBoundingClientRect=function(){const t=performance.now();const r=rect.call(this);const ms=performance.now()-t;p.reads++;p.ms+=ms;if(ms>=2)p.slow.push({ms,cls:this.className,stack:new Error().stack});return r;};
  });
  await page.evaluate(async ({projectId,sessionId,pinId})=>{
    const connectionUrl = "/src/platform/connection/app-connection.ts"; const connection = await import(/* @vite-ignore */ connectionUrl) as typeof import("../src/platform/connection/app-connection.ts");const walkUrl = "/src/files/walk/walk-store.ts"; const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");const client=connection.getLycaonClient();
    if (!client) throw new Error("API client unavailable for history navigation");
    await walk.enterWalk(projectId,{...client,listProjectSourceWalk:(id,options)=>client.listProjectSourceWalk(id,{...options,baseline:`pin:${pinId}`,sessionId:undefined,includeOutsideChanges:undefined})},sessionId);
  },{projectId:project.id,sessionId,pinId});
  const snapshots:unknown[]=[];
  const snap=async(label:string)=>snapshots.push(await page.evaluate(async label=>{
    for(let i=0;i<8;i++)await new Promise(requestAnimationFrame);
    const p=window.historyProbe;return{label,editors:p.editors.size,bars:p.bars.size,reads:p.reads,readMs:p.ms,mounted:document.querySelectorAll(".cm-editor").length};
  },label));
  const samples:unknown[]=[];
  for (const at of [0,1,2,0,2,1,0,1,2,1,0,2]) {
    const start=Date.now();const handler=await page.evaluate(async({projectId,at})=>{const walkUrl = "/src/files/walk/walk-store.ts"; const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");const t=performance.now();walk.setWalkAt(projectId,at);return performance.now()-t;},{projectId:project.id,at});
    await expect(page.getByTestId("file-version-document").filter({visible:true})).toContainText(`after ${names[at]}`);samples.push({at,handlerMs:handler,automationMs:Date.now()-start});await snap(`history-${at}`);
  }
  for(let i=0;i<4;i++) {
    await page.getByTestId("files-tree-file").filter({hasText:"unrelated.ts"}).click();await expect(page.locator('[data-testid="files-editor-host"] .cm-content:visible')).toContainText("before unrelated.ts");await snap("current");
    await page.evaluate(async projectId=>{const walkUrl = "/src/files/walk/walk-store.ts"; const walk = await import(/* @vite-ignore */ walkUrl) as typeof import("../src/files/walk/walk-store.ts");walk.setWalkAt(projectId,2);},project.id);
    await expect(page.getByTestId("file-version-document").filter({visible:true})).toContainText("after c.ts");await snap("history-return");
  }
  writeFileSync(info.outputPath("history.json"),JSON.stringify({samples,snapshots,slow:await page.evaluate(()=>window.historyProbe.slow)},null,2));
  expect((snapshots.at(-1) as {editors:number}).editors).toBeLessThanOrEqual(4);
  expect((snapshots.at(-1) as {bars:number}).bars).toBeLessThanOrEqual(4);
});
