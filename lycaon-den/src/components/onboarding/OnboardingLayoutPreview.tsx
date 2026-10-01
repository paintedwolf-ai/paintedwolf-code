import { For, Show } from "solid-js";

type Width = "10" | "15" | "20" | "25" | "30" | "40" | "55" | "65" | "75" | "85" | "95";
type Tone = "keyword" | "function" | "string" | "type" | "plain";

type TreeRow = { depth: 0 | 1 | 2; width: Width; folder?: boolean; open?: boolean };
type CodeLine = { indent?: 1 | 2; added?: boolean; tokens: ReadonlyArray<readonly [Tone, Width]> };

const TREE: readonly TreeRow[] = [
  { depth: 0, width: "55", folder: true },
  { depth: 1, width: "65", folder: true },
  { depth: 2, width: "55" },
  { depth: 2, width: "75", open: true },
  { depth: 2, width: "40" },
  { depth: 1, width: "55", folder: true },
  { depth: 2, width: "65" },
  { depth: 0, width: "40" },
  { depth: 0, width: "55" },
];

const CODE: readonly CodeLine[] = [
  { tokens: [["keyword", "10"], ["type", "20"], ["plain", "10"], ["string", "25"]] },
  { tokens: [["keyword", "10"], ["type", "15"], ["plain", "10"], ["string", "20"]] },
  { tokens: [] },
  { tokens: [["keyword", "10"], ["function", "20"], ["plain", "30"]] },
  { indent: 1, tokens: [["keyword", "10"], ["plain", "15"], ["type", "10"]] },
  { indent: 1, added: true, tokens: [["keyword", "10"], ["function", "15"], ["plain", "25"]] },
  { indent: 2, added: true, tokens: [["plain", "20"], ["string", "30"]] },
  { indent: 1, added: true, tokens: [["plain", "10"]] },
  { indent: 1, tokens: [["keyword", "10"], ["plain", "20"], ["function", "15"]] },
  { tokens: [["plain", "10"]] },
  { tokens: [] },
  { tokens: [["keyword", "10"], ["function", "25"], ["plain", "20"]] },
  { indent: 1, tokens: [["keyword", "10"], ["plain", "30"]] },
];

function Bar(props: { width: Width; soft?: boolean }) {
  return (
    <i
      class="onboarding-layout-preview__bar"
      data-w={props.width}
      data-tone={props.soft ? "soft" : undefined}
    />
  );
}

function Rail() {
  return (
    <span class="onboarding-layout-preview__rail">
      <span class="onboarding-layout-preview__brand">
        <i class="onboarding-layout-preview__glyph" />
        <Bar width="65" />
      </span>
      <span class="onboarding-layout-preview__new" />
      <span class="onboarding-layout-preview__row onboarding-layout-preview__row--on">
        <Bar width="75" />
      </span>
      <span class="onboarding-layout-preview__row">
        <Bar width="55" soft />
      </span>
      <span class="onboarding-layout-preview__row">
        <Bar width="65" soft />
      </span>
      <span class="onboarding-layout-preview__row">
        <Bar width="40" soft />
      </span>
    </span>
  );
}

function Tree() {
  return (
    <span class="onboarding-layout-preview__tree">
      <For each={TREE}>
        {(row) => (
          <span
            class="onboarding-layout-preview__row"
            classList={{ "onboarding-layout-preview__row--on": row.open === true }}
            data-depth={row.depth}
          >
            <Show when={row.folder}>
              <i class="onboarding-layout-preview__twisty" />
            </Show>
            <Bar width={row.width} soft={row.open !== true} />
          </span>
        )}
      </For>
    </span>
  );
}

function Editor() {
  return (
    <span class="onboarding-layout-preview__editor">
      <span class="onboarding-layout-preview__tabs">
        <span class="onboarding-layout-preview__tab onboarding-layout-preview__tab--on">
          <Bar width="75" />
        </span>
        <span class="onboarding-layout-preview__tab">
          <Bar width="65" soft />
        </span>
      </span>
      <span class="onboarding-layout-preview__code">
        <For each={CODE}>
          {(line) => (
            <span
              class="onboarding-layout-preview__line"
              classList={{ "onboarding-layout-preview__line--added": line.added === true }}
              data-indent={line.indent}
            >
              <For each={line.tokens}>
                {([tone, width]) => (
                  <i
                    class="onboarding-layout-preview__token"
                    data-tone={tone}
                    data-w={width}
                  />
                )}
              </For>
            </span>
          )}
        </For>
      </span>
    </span>
  );
}

function Conversation() {
  return (
    <span class="onboarding-layout-preview__convo">
      <span class="onboarding-layout-preview__thread">
        <span class="onboarding-layout-preview__ask">
          <Bar width="95" />
          <Bar width="55" />
        </span>
        <span class="onboarding-layout-preview__reply">
          <Bar width="95" />
          <Bar width="85" />
          <Bar width="65" />
        </span>
        <span class="onboarding-layout-preview__task">
          <span class="onboarding-layout-preview__step">
            <i class="onboarding-layout-preview__check" />
            <Bar width="40" soft />
          </span>
          <span class="onboarding-layout-preview__step">
            <i class="onboarding-layout-preview__check" />
            <Bar width="55" soft />
          </span>
          <span class="onboarding-layout-preview__step">
            <i class="onboarding-layout-preview__check onboarding-layout-preview__check--running" />
            <Bar width="30" soft />
          </span>
        </span>
        <span class="onboarding-layout-preview__reply">
          <Bar width="85" />
          <Bar width="40" />
        </span>
      </span>
      <span class="onboarding-layout-preview__composer">
        <Bar width="40" soft />
        <i class="onboarding-layout-preview__send" />
      </span>
    </span>
  );
}

/** A miniature window in the live theme, mirrored with the workspace. */
export function OnboardingLayoutPreview(props: {
  kind: "chat" | "split";
  mirrored: boolean;
  chatFirst?: boolean;
}) {
  return (
    <span
      class="onboarding-layout-preview"
      classList={{
        "onboarding-layout-preview--chat": props.kind === "chat",
        "onboarding-layout-preview--split": props.kind === "split",
        "onboarding-layout-preview--mirrored": props.mirrored,
        "onboarding-layout-preview--chat-first": props.kind === "split" && props.chatFirst,
      }}
      data-testid={`onboarding-layout-preview-${props.kind}`}
    >
      <span class="onboarding-layout-preview__titlebar">
        <i />
        <i />
        <i />
      </span>
      <span class="onboarding-layout-preview__body">
        <Rail />
        <Show when={props.kind === "split"}>
          <Tree />
          <Editor />
        </Show>
        <Conversation />
      </span>
    </span>
  );
}
