import { For, Show, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { WalkGitReview, type OpenGitReviewFile } from "./WalkGitReview.tsx";
import type { SourceWalkEffect } from "../../api/types.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import { createResidentActivity } from "../../ui/resident-activity.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { setWalkGitSelection } from "../documents/project-files-buffers.ts";
import { type FileBuffer } from "../documents/files-buffer-state.ts";
import { countLabel, fileBasename, fileDirname, opGlyph } from "../review/review-model.ts";
import { wholeFileOpChange } from "../../components/source/reader/source-reader-change.ts";
import {
  commandWindowAdmissionLabel,
  commandWindowLine,
  commandWindowObservation,
  commandWindowStateNote,
} from "../source/source-command-window.ts";
import { gitChangeRefs, gitChangeTitle } from "../source/source-git-change.ts";
import { walkChapterAt } from "./walk-chapters.ts";
import { walkGroupGlyph, walkObservedSpan, walkStepFiles, walkStepOperation, type WalkGroupStep, type WalkGitStep } from "./walk-model.ts";
import { subscribeWalk, walkState } from "./walk-store.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";

const WALK_OPEN_FAILURE = {
  code: "walk_version_open_failed",
  title: "Could not open the version",
  suggestedAction: "Pick the version again, or open the file from the tree.",
};

type Props = {
  projectId: string;
  buffer: FileBuffer;
  client: LycaonClient | null;
  sessionId?: string;
  rootLabelFor?: (rootId: string) => string | undefined;
  onOpenGitFile: OpenGitReviewFile;
  onOpenFile: (step: WalkGroupStep, effect: SourceWalkEffect) => Promise<void>;
};

const TAB_COMMAND_LIMIT = 40;

export function walkPageTitle(step: WalkGroupStep): string {
  switch (step.kind) {
    case "git": {
      const refs = gitChangeRefs(step.change);
      return refs ? `${gitChangeTitle(step.change)} · ${refs}` : gitChangeTitle(step.change);
    }
    case "command": {
      const line = commandWindowLine(step.command);
      return line.length > TAB_COMMAND_LIMIT ? `${line.slice(0, TAB_COMMAND_LIMIT - 1)}…` : line;
    }
    case "outside":
      return walkStepOperation(step);
  }
}

type ActivityStep = Exclude<WalkGroupStep, { kind: "git" }>;

function pageSubtitle(step: ActivityStep): string {
  switch (step.kind) {
    case "command":
      return commandWindowLine(step.command);
    case "outside":
      return walkObservedSpan(step.effects);
  }
}

function pageLede(step: ActivityStep): string {
  switch (step.kind) {
    case "command":
      return commandWindowObservation();
    case "outside":
      return "Files that changed outside the app. Nothing in this chat wrote them.";
  }
}

function filesTitle(step: ActivityStep): string {
  switch (step.kind) {
    case "command":
      return "Files observed changing";
    case "outside":
      return "Files changed";
  }
}

export function WalkStepPage(props: Props) {
  const [tick, setTick] = createSignal(0);
  const [opening, setOpening] = createSignal<string | null>(null);
  let openAttempt = 0;
  const openFile = (step: WalkGroupStep, effect: SourceWalkEffect) => {
    const attempt = ++openAttempt;
    setOpening(effect.id);
    props.onOpenFile(step, effect).then(
      () => {
        if (attempt === openAttempt) setOpening(null);
      },
      (error: unknown) => {
        if (attempt !== openAttempt) return;
        setOpening(null);
        reportSurfaceFailure(WALK_OPEN_FAILURE, error instanceof Error && error.message.trim()
          ? `Couldn't open this version: ${error.message.trim()}`
          : "Couldn't open this version.", props.projectId);
      },
    );
  };
  createResidentActivity(() => {
    setTick((value) => value + 1);
    return subscribeWalk((id) => {
      if (id === props.projectId.trim()) setTick((value) => value + 1);
    });
  });
  // The opened snapshot keeps the page available after the walk closes.
  const live = createMemo(() => {
    void tick();
    const snapshot = props.buffer.walkStep;
    const state = walkState(props.projectId);
    const index = snapshot
      ? state.walk.steps.findIndex((step) => step.key === snapshot.key)
      : -1;
    const held = index >= 0 ? state.walk.steps[index] : undefined;
    return {
      active: state.active,
      index,
      count: state.walk.steps.length,
      chapter: index >= 0 ? walkChapterAt(state.walk, index) : null,
      step: held && held.kind !== "effect" ? held : snapshot ?? null,
    };
  });
  const files = createMemo(() => {
    const step = live().step;
    return step ? walkStepFiles(step) : [];
  });

  return (
    <div class="den-files-info" data-testid="walk-page">
      {/* A page shows its own position instead of editor chrome. */}
      <div class="den-walk-page-position">
        <span class="den-walk-page__crumb" data-testid="walk-page-position">
          <ThemeIcon slot="walk" size={14} />
          <Show
            when={live().active && live().index >= 0}
            fallback={<span>Walk closed</span>}
          >
            <strong>Step {live().index + 1} of {live().count}</strong>
            <Show when={live().chapter?.title}>
              {(title) => <span>· {title()}</span>}
            </Show>
          </Show>
        </span>
      </div>
      <ShowLatest when={live().step} by={(step) => step.kind}>
        {(latest) => {
          const step = () => latest() as ActivityStep;
          const git = () => latest() as WalkGitStep;
          const command = () => {
            const current = latest();
            return current.kind === "command" ? current.command : null;
          };
          const commandNote = () => {
            const current = command();
            return current ? commandWindowStateNote(current) : null;
          };
          return latest().kind === "git" ? (
          <WalkGitReview projectId={props.projectId} client={props.client} sessionId={props.sessionId} rootLabel={props.rootLabelFor?.(git().change.root_id)}
            selection={props.buffer.walkGitSelection}
            onSelectionChange={(selection) => setWalkGitSelection(props.projectId, props.buffer.key, selection)}
            step={git()} onOpenGitFile={(...args) => void props.onOpenGitFile(...args)} onOpenFile={(...args) => void props.onOpenFile(...args)} />
        ) : (
          <Scrollport class="den-files-info__body">
            <article class="den-walk-page__article">
              <header class="den-walk-page__head">
                <span class="den-walk-page__glyph" aria-hidden="true">
                  {walkGroupGlyph(step())}
                </span>
                <div class="den-walk-page__heading">
                  <h1 class="den-walk-page__title" data-testid="walk-page-title">
                    {walkStepOperation(step())}
                  </h1>
                  <Show when={pageSubtitle(step())}>
                    {(subtitle) => (
                      <p
                        class="den-walk-page__subtitle"
                        classList={{ "den-walk-page__subtitle--mono": step().kind === "command" }}
                        data-testid="walk-page-subtitle"
                      >
                        {subtitle()}
                      </p>
                    )}
                  </Show>
                </div>
              </header>

              <p class="den-walk-page__lede" data-testid="walk-page-lede">
                {pageLede(step())}
              </p>
              <Show when={commandNote()}>
                {(note) => (
                  <p class="den-walk-page__note" data-testid="walk-page-state">
                    {note()}
                  </p>
                )}
              </Show>

              <dl
                class="den-files-info__meta den-walk-page__meta"
                aria-label="Step details"
                data-testid="walk-page-meta"
              >
                <Show when={command()} keyed>
                  {(command) => (
                    <>
                      <div class="den-files-info__meta-row">
                        <dt>Tool</dt>
                        <dd>{command.tool_name}</dd>
                      </div>
                      <Show when={command.turn > 0}>
                        <div class="den-files-info__meta-row">
                          <dt>Turn</dt>
                          <dd>{command.turn}</dd>
                        </div>
                      </Show>
                      <Show when={commandWindowAdmissionLabel(command)}>
                        {(admission) => (
                          <div class="den-files-info__meta-row">
                            <dt>Counted</dt>
                            <dd>{admission()}</dd>
                          </div>
                        )}
                      </Show>
                      <Show when={relativeTimeLabel(command.started_at)}>
                        {(when) => (
                          <div class="den-files-info__meta-row">
                            <dt>Started</dt>
                            <dd>{when()}</dd>
                          </div>
                        )}
                      </Show>
                    </>
                  )}
                </Show>
                <Show when={step().kind === "outside"}>
                  <div class="den-files-info__meta-row">
                    <dt>Observed</dt>
                    <dd>{walkObservedSpan(step().effects) || "Unknown"}</dd>
                  </div>
                </Show>
                <div class="den-files-info__meta-row">
                  <dt>Files</dt>
                  <dd>{files().length}</dd>
                </div>
              </dl>

              <section class="den-walk-page__files" aria-labelledby="walk-page-files-title">
                <h2 class="den-walk-page__files-title" id="walk-page-files-title">
                  {filesTitle(step())}
                  <Show when={files().length > 0}>
                    <span class="den-walk-page__count">{countLabel(files().length, "file")}</span>
                  </Show>
                </h2>
                <Show
                  when={files().length > 0}
                  fallback={
                    <p class="den-walk-page__empty" data-testid="walk-page-empty">
                      No file changes were observed.
                    </p>
                  }
                >
                  <ul class="den-walk-page__list" data-testid="walk-page-files">
                    <For each={files()}>
                      {(effect) => (
                        <li>
                          <button
                            type="button"
                            class="den-walk-page__file"
                            data-testid="walk-page-file"
                            data-path={effect.path}
                            aria-busy={opening() === effect.id}
                            onClick={() => openFile(step(), effect)}
                          >
                            <span class="den-walk-page__op" aria-hidden="true">
                              {opGlyph(effect.op)}
                            </span>
                            <span class="den-walk-page__name" data-file-change={wholeFileOpChange(effect.op)}>{fileBasename(effect.path)}</span>
                            <span class="den-walk-page__dir">{fileDirname(effect.path)}</span>
                            <span class="den-walk-page__open" aria-hidden="true">
                              {opening() === effect.id ? "Opening…" : "Open this version"}
                            </span>
                          </button>
                        </li>
                      )}
                    </For>
                  </ul>
                </Show>
              </section>
            </article>
          </Scrollport>
        ); }}
      </ShowLatest>
    </div>
  );
}
