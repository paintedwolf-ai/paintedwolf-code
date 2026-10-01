import { Show } from "solid-js";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import type { TurnLoadMatchBy } from "../../api/types.ts";
import { hasDiscoveryOutput } from "../../chat/tool/structured/discovery.ts";
import { useToolLoadFact } from "./turn-load-context.tsx";

function resolutionMethod(by: TurnLoadMatchBy): string {
  switch (by) {
    case "engine": return "Local AI";
    case "name": return "Exact name";
    case "none": return "No match";
  }
}

/** Distinguishes the agent's lookup from the host method that loaded it. */
export function ToolResolutionFacts(props: { part: ToolPartView }) {
  const load = useToolLoadFact(() => props.part);
  const match = () => {
    const fact = load();
    if (fact?.kind !== "matched") return undefined;
    return fact.by === "none" && hasDiscoveryOutput(props.part.output ?? "") ? undefined : fact;
  };
  return (
    <Show when={match()} keyed>
      {(fact) => (
        <div class="den-tool-part-card-section" data-testid="tool-resolution-facts">
          <dl class="den-tool-part-card-facts">
            <div>
              <dt>{fact.by === "none" ? "Resolution" : "Loaded by"}</dt>
              <dd data-testid="tool-resolution-method">{resolutionMethod(fact.by)}</dd>
            </div>
          </dl>
        </div>
      )}
    </Show>
  );
}
