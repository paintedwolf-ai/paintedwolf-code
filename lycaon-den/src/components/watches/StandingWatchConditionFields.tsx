import { For, Show } from "solid-js";
import type { WatchCalendarSchedule, WatchCondition } from "../../api/types.ts";
import { formatEveryMinutes } from "../../watches/watch-format.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenNumberInput } from "../primitives/DenNumberInput.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";

type TimerSubkind = "every_minutes" | "calendar" | "at";

const CONDITION_KIND_OPTIONS = [
  { value: "timer", label: "Timer" },
  { value: "git_ref", label: "Git ref" },
  { value: "file_change", label: "File change" },
] as const;

const TIMER_MODE_OPTIONS = [
  { value: "every_minutes", label: "Interval" },
  { value: "calendar", label: "Calendar" },
  { value: "at", label: "One-time date" },
] as const;

const CALENDAR_DAY_OPTIONS = [
  { value: "weekdays", label: "Weekdays" },
  { value: "every_day", label: "Every day" },
  { value: "monday", label: "Monday" },
  { value: "tuesday", label: "Tuesday" },
  { value: "wednesday", label: "Wednesday" },
  { value: "thursday", label: "Thursday" },
  { value: "friday", label: "Friday" },
  { value: "saturday", label: "Saturday" },
  { value: "sunday", label: "Sunday" },
] as const;

function timerSubkindOf(c: WatchCondition): TimerSubkind {
  if (c.calendar) return "calendar";
  if (c.at) return "at";
  return "every_minutes";
}

type Props = {
  conditions: readonly WatchCondition[];
  highlighted?: boolean;
  onChange: (next: WatchCondition[]) => void;
};

export function StandingWatchConditionFields(props: Props) {
  const updateAt = (index: number, nextCond: WatchCondition) => {
    const copy = props.conditions.map((c, i) => (i === index ? nextCond : c));
    props.onChange(copy);
  };

  const removeAt = (index: number) => {
    if (props.conditions.length <= 1) return;
    props.onChange(props.conditions.filter((_, i) => i !== index));
  };

  const addCondition = () => {
    if (props.conditions.length >= 8) return;
    props.onChange([
      ...props.conditions,
      { kind: "timer", every_minutes: 60 },
    ]);
  };

  return (
    <fieldset
      class="flex flex-col gap-den-2 rounded-den-sm border p-den-2-5"
      classList={{
        "border-den-accent": props.highlighted === true,
        "border-den-line": props.highlighted !== true,
      }}
      data-testid="standing-watch-conditions-fieldset"
    >
      <legend class="px-den-1 text-den-caption font-medium text-den-text-primary">
        Conditions {props.highlighted ? "(changed)" : ""}
      </legend>

      <For each={props.conditions}>
        {(cond, idx) => {
          const kind = () => cond.kind;
          const subkind = () => timerSubkindOf(cond);

          return (
            <div
              class="flex flex-wrap items-end gap-den-2 rounded-den-sm border border-den-line p-den-2"
              data-testid={`standing-watch-condition-${idx()}`}
            >
              <div class="flex flex-col gap-den-1 text-den-caption">
                <span>Kind</span>
                <DenSelect
                  aria-label={`Condition ${idx() + 1} kind`}
                  options={CONDITION_KIND_OPTIONS}
                  value={kind()}
                  onValueChange={(v) => {
                    if (v === "git_ref") {
                      updateAt(idx(), { kind: "git_ref", ref: "refs/heads/main" });
                    } else if (v === "file_change") {
                      updateAt(idx(), { kind: "file_change", path: "src" });
                    } else {
                      updateAt(idx(), { kind: "timer", every_minutes: 60 });
                    }
                  }}
                />
              </div>

              <Show when={kind() === "timer"}>
                <div class="flex flex-col gap-den-1 text-den-caption">
                  <span>Schedule</span>
                  <DenSelect
                    aria-label={`Condition ${idx() + 1} timer mode`}
                    options={TIMER_MODE_OPTIONS}
                    value={subkind()}
                    onValueChange={(v) => {
                      const mode = v as TimerSubkind;
                      if (mode === "calendar") {
                        updateAt(idx(), {
                          kind: "timer",
                          calendar: { days: ["weekdays"], times: ["09:00"] },
                        });
                      } else if (mode === "at") {
                        updateAt(idx(), {
                          kind: "timer",
                          at: "2027-01-01T09:00",
                        });
                      } else {
                        updateAt(idx(), { kind: "timer", every_minutes: 60 });
                      }
                    }}
                  />
                </div>

                <Show when={subkind() === "every_minutes"}>
                  <label class="flex flex-col gap-den-1 text-den-caption">
                    <span>
                      Minutes ({formatEveryMinutes(cond.every_minutes ?? 60)})
                    </span>
                    <DenNumberInput
                      min={15}
                      max={10080}
                      class="w-28"
                      aria-label={`Condition ${idx() + 1} every minutes`}
                      value={cond.every_minutes ?? 60}
                      onNumber={(num) =>
                        updateAt(idx(), {
                          kind: "timer",
                          every_minutes: Math.max(15, num ?? 15),
                        })
                      }
                    />
                  </label>
                </Show>

                <Show when={subkind() === "calendar"}>
                  <div class="flex flex-col gap-den-1 text-den-caption">
                    <span>Days</span>
                    <DenSelect
                      aria-label={`Condition ${idx() + 1} calendar days`}
                      options={CALENDAR_DAY_OPTIONS}
                      value={cond.calendar?.days[0] ?? "weekdays"}
                      onValueChange={(v) => {
                        const day = v as WatchCalendarSchedule["days"][number];
                        updateAt(idx(), {
                          kind: "timer",
                          calendar: {
                            days: [day],
                            times: cond.calendar?.times ?? ["09:00"],
                          },
                        });
                      }}
                    />
                  </div>

                  <label class="flex flex-col gap-den-1 text-den-caption">
                    <span>Times (HH:MM, comma-separated)</span>
                    <input
                      type="text"
                      class="w-36 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 text-den-body"
                      aria-label={`Condition ${idx() + 1} calendar times`}
                      value={(cond.calendar?.times ?? ["09:00"]).join(", ")}
                      onInput={(e) => {
                        const times = e.currentTarget.value
                          .split(",")
                          .map((s) => s.trim())
                          .filter(Boolean);
                        updateAt(idx(), {
                          kind: "timer",
                          calendar: {
                            days: cond.calendar?.days ?? ["weekdays"],
                            times: times.length > 0 ? times : ["09:00"],
                          },
                        });
                      }}
                    />
                  </label>
                </Show>

                <Show when={subkind() === "at"}>
                  <label class="flex flex-col gap-den-1 text-den-caption">
                    <span>Run at (YYYY-MM-DDTHH:MM)</span>
                    <input
                      type="text"
                      class="w-44 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 text-den-body"
                      aria-label={`Condition ${idx() + 1} at time`}
                      value={cond.at ?? ""}
                      onInput={(e) =>
                        updateAt(idx(), {
                          kind: "timer",
                          at: e.currentTarget.value.trim(),
                        })
                      }
                    />
                  </label>
                </Show>
              </Show>

              <Show when={kind() === "git_ref"}>
                <label class="flex flex-col gap-den-1 text-den-caption">
                  <span>Git ref</span>
                  <input
                    type="text"
                    class="w-48 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 font-mono text-den-body"
                    aria-label={`Condition ${idx() + 1} git ref`}
                    value={cond.ref ?? "refs/heads/main"}
                    onInput={(e) =>
                      updateAt(idx(), {
                        ...cond,
                        kind: "git_ref",
                        ref: e.currentTarget.value.trim(),
                      })
                    }
                  />
                </label>
                <label class="flex flex-col gap-den-1 text-den-caption">
                  <span>Folder label (optional)</span>
                  <input
                    type="text"
                    class="w-32 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 text-den-body"
                    aria-label={`Condition ${idx() + 1} root`}
                    value={cond.root_id ?? ""}
                    onInput={(e) => {
                      const root = e.currentTarget.value.trim();
                      updateAt(idx(), {
                        kind: "git_ref",
                        ref: cond.ref ?? "refs/heads/main",
                        ...(root ? { root_id: root } : {}),
                      });
                    }}
                  />
                </label>
              </Show>

              <Show when={kind() === "file_change"}>
                <label class="flex flex-col gap-den-1 text-den-caption">
                  <span>Path</span>
                  <input
                    type="text"
                    class="w-40 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 font-mono text-den-body"
                    aria-label={`Condition ${idx() + 1} file path`}
                    value={cond.path ?? ""}
                    onInput={(e) =>
                      updateAt(idx(), {
                        ...cond,
                        kind: "file_change",
                        path: e.currentTarget.value.trim(),
                      })
                    }
                  />
                </label>
                <label class="flex flex-col gap-den-1 text-den-caption">
                  <span>Glob (optional)</span>
                  <input
                    type="text"
                    class="w-32 rounded-den-sm border border-den-line bg-den-bg-base px-den-2 py-den-1 font-mono text-den-body"
                    aria-label={`Condition ${idx() + 1} glob`}
                    value={cond.glob ?? ""}
                    onInput={(e) => {
                      const glob = e.currentTarget.value.trim();
                      const next: WatchCondition = {
                        kind: "file_change",
                        path: cond.path ?? "",
                        ...(cond.root_id ? { root_id: cond.root_id } : {}),
                        ...(glob ? { glob } : {}),
                      };
                      updateAt(idx(), next);
                    }}
                  />
                </label>
              </Show>

              <Show when={props.conditions.length > 1}>
                <DenButton
                  variant="ghost"
                  compact
                  onClick={() => removeAt(idx())}
                >
                  Remove
                </DenButton>
              </Show>
            </div>
          );
        }}
      </For>

      <Show when={props.conditions.length < 8}>
        <div>
          <DenButton
            variant="ghost"
            compact
            data-testid="standing-watch-add-condition"
            onClick={addCondition}
          >
            Add condition
          </DenButton>
        </div>
      </Show>
    </fieldset>
  );
}
