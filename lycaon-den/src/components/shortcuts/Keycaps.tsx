import { For, Show } from "solid-js";
import {
  displayBindingPartGroups,
  isSequenceBinding,
} from "../../shortcuts/chord.ts";
import {
  displayChordParts,
  shortcutPlatform,
} from "../../shortcuts/platform.ts";
import type { TauriPlatform } from "../../platform/runtime.ts";

/** Platform keycap pills for a list of canonical bindings (chords or sequences). */
export function Keycaps(props: {
  chords: readonly string[];
  platform?: TauriPlatform;
}) {
  const platform = () => props.platform ?? shortcutPlatform();
  return (
    <span class="den-keycaps">
      <Show
        when={props.chords.length > 0}
        fallback={<span class="den-keycap den-keycap--empty">—</span>}
      >
        <For each={props.chords}>
          {(binding, chordIndex) => (
            <>
              <Show when={chordIndex() > 0}>
                {/* Audible separator between alternate chords. */}
                <span class="den-keycaps__or" aria-hidden="true">
                  /
                </span>
                <span class="sr-only">or</span>
              </Show>
              <Show
                when={isSequenceBinding(binding)}
                fallback={
                  <span class="den-keycaps__chord">
                    <For each={displayChordParts(binding, platform())}>
                      {(part) => <kbd class="den-keycap">{part}</kbd>}
                    </For>
                  </span>
                }
              >
                <span class="den-keycaps__sequence">
                  <For each={displayBindingPartGroups(binding, platform())}>
                    {(step, stepIndex) => (
                      <>
                        <Show when={stepIndex() > 0}>
                          {/* Audible sequence-step separator. */}
                          <span class="den-keycaps__then">then</span>
                        </Show>
                        <span class="den-keycaps__chord">
                          <For each={step}>
                            {(part) => <kbd class="den-keycap">{part}</kbd>}
                          </For>
                        </span>
                      </>
                    )}
                  </For>
                </span>
              </Show>
            </>
          )}
        </For>
      </Show>
    </span>
  );
}
