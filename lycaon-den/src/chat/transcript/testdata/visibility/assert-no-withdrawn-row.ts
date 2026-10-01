import type { Message } from "../../../../api/types.ts";
import { latestMessageSnapshot } from "../../projection/messages-equal.ts";
import { createTranscriptDisplayProjector } from "../../projection/transcript-display-projection.ts";
import {
  visibleRowContinuity,
  withdrawnRows,
} from "../../presentation/row-withdrawal-watch.ts";
import fixtureJson from "./fixture.json";

/** Adversarial dispatch frames preserve visibility fields while clipping bodies. */
export type VisibilityFixture = {
  frames: { message: Message }[];
};

export function loadVisibilityFixture(): VisibilityFixture {
  return fixtureJson as unknown as VisibilityFixture;
}

export type WithdrawnRow = {
  /** Rendered row that disappeared. */
  key: string;
  /** 1-based frame that withdrew it. */
  frame: number;
  /** The wire row whose arrival withdrew it. */
  cause: string;
};

/** Replay frames one at a time, reporting steps that lost their final row. */
export function withdrawnRowsDuringReplay(
  frames: readonly { message: Message }[],
  opts: { verboseMode?: boolean } = {},
): WithdrawnRow[] {
  const store = new Map<string, Message>();
  const withdrawn: WithdrawnRow[] = [];
  let previous = new Map<string, string>();

  frames.forEach((frame, index) => {
    const existing = store.get(frame.message.id);
    store.set(
      frame.message.id,
      existing ? latestMessageSnapshot(existing, frame.message) : frame.message,
    );
    const items = createTranscriptDisplayProjector()([...store.values()], undefined, {
      verboseMode: opts.verboseMode ?? false,
      layout: "chat",
    })[0]!;
    const current = visibleRowContinuity(items);
    // Replay never pages, so every key seen so far is still retained.
    const retained = new Set([...previous.keys(), ...current.keys()]);
    for (const row of withdrawnRows(previous, current, retained)) {
      const result = frame.message.tool_result;
      withdrawn.push({
        key: row.key,
        frame: index + 1,
        cause: result?.tool
          ? `${frame.message.role} ${result.tool} (ui_visibility=${result.ui_visibility ?? "unset"}, outcome=${result.outcome ?? "unset"})`
          : `${frame.message.role} ${frame.message.id}`,
      });
    }
    previous = current;
  });

  return withdrawn;
}
