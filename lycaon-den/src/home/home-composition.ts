import { batch, createEffect, createSignal, on, onCleanup, untrack } from "solid-js";
import type { SessionMode } from "../api/types.ts";
import { afterPaint } from "../ui/surface-reveal.ts";
import { launchChipsFromIdea, revokeHomeIdeaAttachment, type HomeIdeaAttachment } from "./idea-attachments.ts";
import type { CompositionRecord } from "./launch/home-launch-model.ts";
import { readLaunchBytes, saveComposition } from "./launch/home-launches.ts";

/** What Enter hands to the launch: the frozen revision of one composition. */
export type FrozenComposition = {
  compositionId: string;
  compositionRevision: number;
  text: string;
  attachments: HomeIdeaAttachment[];
};

/**
 * The editable Home idea with a persistent identity. Every edit advances its
 * revision and is saved after paint, latest revision last; submission freezes
 * one revision and starts a fresh composition.
 */
export function createHomeComposition(options: { mode: () => SessionMode | undefined }) {
  const [id, setId] = createSignal<string>(crypto.randomUUID());
  const [revision, setRevision] = createSignal(0);
  const [text, setText] = createSignal("");
  const [attachments, setAttachments] = createSignal<HomeIdeaAttachment[]>([]);
  let cancelSave: (() => void) | undefined;

  const record = (): CompositionRecord | null => {
    if (!text() && attachments().length === 0) return null;
    return {
      id: id(),
      revision: revision(),
      text: text(),
      ...(options.mode() ? { mode: options.mode() } : {}),
      chips: launchChipsFromIdea(attachments()),
      updatedAt: Date.now(),
    };
  };

  createEffect(on([text, attachments, options.mode], () => {
    setRevision((n) => n + 1);
    cancelSave?.();
    cancelSave = afterPaint(() => {
      void saveComposition(untrack(record)).catch(() => undefined);
    });
  }, { defer: true }));
  onCleanup(() => cancelSave?.());

  /** Freezes the current revision and starts a new, empty composition. */
  const take = (): FrozenComposition => {
    const frozen: FrozenComposition = {
      compositionId: id(),
      compositionRevision: revision(),
      text: text(),
      attachments: attachments(),
    };
    batch(() => {
      setId(crypto.randomUUID());
      setText("");
      setAttachments([]);
      setRevision(0);
    });
    return frozen;
  };

  /** Restores the last saved composition into an empty Home; never sends it. */
  const restore = async (saved: CompositionRecord | undefined) => {
    if (!saved || text() || attachments().length > 0) return;
    const restored: HomeIdeaAttachment[] = [];
    for (const chip of saved.chips) {
      if (chip.kind === "bytes") {
        const bytes = await readLaunchBytes(chip.itemId).catch(() => undefined);
        restored.push(bytes
          ? { id: chip.itemId, kind: "web-file", name: chip.name, file: new File([bytes], chip.name, { type: chip.mime }), mime: chip.mime, byteLength: chip.byteLength }
          : { id: chip.itemId, kind: "reject", name: chip.name, reason: "Choose this file again; its copy was not kept." });
      } else if (chip.kind === "path") {
        restored.push({
          id: chip.itemId, kind: "path", name: chip.name, absolutePath: chip.absolutePath, expected: chip.expected,
          ...(chip.fingerprint ? { fingerprint: chip.fingerprint } : {}),
        });
      } else {
        restored.push({ id: chip.itemId, kind: "reject", name: chip.name, reason: chip.reason });
      }
    }
    if (text() || attachments().length > 0) return;
    batch(() => {
      setId(saved.id);
      setText(saved.text);
      setAttachments(restored);
      setRevision(saved.revision);
    });
  };

  const clearAttachments = () => {
    for (const item of attachments()) revokeHomeIdeaAttachment(item);
    setAttachments([]);
  };

  return { id, revision, text, setText, attachments, setAttachments, clearAttachments, take, restore };
}
