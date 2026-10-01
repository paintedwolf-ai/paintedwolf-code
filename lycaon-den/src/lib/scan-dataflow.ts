import type { SecurityFindingCallTrace, SecurityFindingDataflow, SecurityFindingLocation } from "../api/types.ts";

export type DataflowStep = {
  kind: "Source" | "Propagation" | "Return" | "Call" | "Sink";
  location: SecurityFindingLocation;
  depth: number;
};

export function dataflowSteps(flow: SecurityFindingDataflow): DataflowStep[] {
  const steps: DataflowStep[] = [];
  const source = (call: SecurityFindingCallTrace, depth: number) => {
    if (!call.callee) {
      steps.push({ kind: "Source", location: call.location, depth });
      return;
    }
    source(call.callee, depth + 1);
    for (const location of call.intermediates ?? []) steps.push({ kind: "Propagation", location, depth: depth + 1 });
    steps.push({ kind: "Return", location: call.location, depth });
  };
  if (flow.source) source(flow.source, 0);
  for (const location of flow.intermediates ?? []) steps.push({ kind: "Propagation", location, depth: 0 });
  let depth = 0;
  for (let call = flow.sink; call; call = call.callee) {
    steps.push({ kind: call.callee ? "Call" : "Sink", location: call.location, depth });
    depth++;
    for (const location of call.intermediates ?? []) steps.push({ kind: "Propagation", location, depth });
  }
  return steps;
}
