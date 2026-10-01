import { fireEvent, render } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { ApprovalContentLink, ApprovalContentProvider } from "./ApprovalContentLink.tsx";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../../platform/navigation/open-files-surface.ts";

afterEach(resetOpenFilesSurfaceForTests);

it("builds complete approval text only on opening and uses the current checkpoint identity", () => {
  const sink = vi.fn();
  registerOpenFilesSurfaceSink(sink);
  const text = vi.fn(() => "Complete target list\n".repeat(1000));
  const [checkpointId, setCheckpointId] = createSignal("first");
  const view = render(() => <ApprovalContentProvider value={() => ({checkpointId:checkpointId(),projectId:"project",sessionId:"worker-session"})}>
    <ApprovalContentLink label="Approval details" pane="details" text={text} />
  </ApprovalContentProvider>);
  expect(text).not.toHaveBeenCalled();
  expect(view.container.textContent).not.toContain("Complete target list");
  fireEvent.click(view.getByRole("button", {name:"Approval details in Files"}));
  expect(text).toHaveBeenCalledTimes(1);
  expect(sink).toHaveBeenLastCalledWith({kind:"chat-content",projectId:"project",document:{
    kind:"approval",checkpointId:"first",sessionId:"worker-session",pane:"details",title:"Approval details",
    content:{kind:"inline",text:"Complete target list\n".repeat(1000)},
  }});
  setCheckpointId("second");
  fireEvent.click(view.getByRole("button", {name:"Approval details in Files"}));
  expect(sink.mock.lastCall?.[0].document.checkpointId).toBe("second");
});
