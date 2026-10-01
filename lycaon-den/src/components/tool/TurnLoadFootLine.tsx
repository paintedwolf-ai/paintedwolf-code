import { Show } from "solid-js";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { LOCAL_AI_LABEL, formatProbability } from "../../chat/turnload/turn-load-rows.ts";
import { useToolLoadFact } from "./turn-load-context.tsx";

/** One line at the foot of a tool card: how the local AI bore on this call. */
export function TurnLoadFootLine(props: { part: ToolPartView }) {
  const fact = useToolLoadFact(() => props.part);
  return (
    <Show when={fact()} keyed>
      {(loaded) => (
        <p class="den-tool-part-load" data-testid="tool-part-load" data-load-kind={loaded.kind}>
          <Show when={loaded.kind === "offered" ? loaded : undefined} keyed>
            {(offered) => (
              <>
                <span>Offered by {LOCAL_AI_LABEL} at the start of the turn</span>
                <span class="den-tool-part-load-sep" aria-hidden="true">·</span>
                <span>P {formatProbability(offered.p)}</span>
              </>
            )}
          </Show>
          <Show when={loaded.kind === "kept"}>
            <span>Kept from an earlier turn while the prompt cache held</span>
          </Show>
          <Show when={loaded.kind === "companion" ? loaded : undefined} keyed>
            {(companion) => (
              <span>
                Offered with <code>{companion.with}</code>, which {LOCAL_AI_LABEL} predicted at the start of the turn
              </span>
            )}
          </Show>
          <Show when={loaded.kind === "requested" ? loaded : undefined} keyed>
            {(requested) => (
              <span>
                Offered on the model's request: <q>{requested.need}</q>
              </span>
            )}
          </Show>
          <Show when={loaded.kind === "matched" ? loaded : undefined} keyed>
            {(matched) => (
              <>
                <span>Agent requested: <q>{matched.need}</q></span>
                <Show when={matched.missed}>
                  <span class="den-tool-part-load-sep" aria-hidden="true">·</span>
                  <span>Not among the tools {LOCAL_AI_LABEL} offered at the start of the turn</span>
                </Show>
              </>
            )}
          </Show>
          <Show when={loaded.kind === "skill" ? loaded : undefined} keyed>
            {(skill) => (
              <span>
                {skill.read ? `${LOCAL_AI_LABEL} read skill` : `${LOCAL_AI_LABEL} suggested skill`} <q>{skill.name}</q> for this call
              </span>
            )}
          </Show>
        </p>
      )}
    </Show>
  );
}
