import { createContext, createSignal, useContext, type Accessor } from "solid-js";
import { useTranscriptViewport } from "../stream/transcript-viewport.tsx";
import { animateDisclosureHeight } from "../transcript/presentation/transcript-disclosure.tsx";
import { isTranscriptDisclosureOpen } from "../transcript/presentation/transcript-disclosure-shell.ts";
import { createRowAccordion } from "../transcript/presentation/row-accordion.ts";
import type { RowAccordion } from "../transcript/presentation/row-accordion.ts";
import type { TranscriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";

export interface ActivitySpanRowHandle {
  key: string;
  disclosureKey: TranscriptDisclosureKey;
  shell: () => HTMLElement | null;
  header: () => HTMLElement | null;
  applyOpen: (next: boolean) => void;
}

export interface ActivitySpanAccordion {
  activeKey: Accessor<string | null>;
  register: (handle: ActivitySpanRowHandle) => () => void;
  open: (key: string) => void;
  toggle: (key: string) => void;
}

const ActivitySpanAccordionContext = createContext<ActivitySpanAccordion>();

export const ActivitySpanAccordionProvider = ActivitySpanAccordionContext.Provider;

export function useActivitySpanAccordion(): ActivitySpanAccordion | undefined {
  return useContext(ActivitySpanAccordionContext);
}

export function createActivitySpanAccordion(): ActivitySpanAccordion {
  const viewport = useTranscriptViewport();
  const accordion: RowAccordion = createRowAccordion();
  const [activeKey, setActiveKey] = createSignal<string | null>(null);
  return {
    activeKey,
    register: (handle) =>
      accordion.register({
        key: handle.key,
        // Incomplete handles stay outside accordion motion.
        el: () => (handle.header() ? handle.shell() : null),
        domOpen: () => {
          const shell = handle.shell();
          return shell ? isTranscriptDisclosureOpen(shell) : false;
        },
        apply: (next) => {
          const shell = handle.shell();
          const header = handle.header();
          if (!shell || !header) return;
          const direction = next ? "open" : "close";
          const finishMotion = viewport?.beginDisclosureMotion(handle.disclosureKey, direction);
          animateDisclosureHeight(
            shell,
            header,
            handle.applyOpen,
            {
              direction,
              onSettled: finishMotion,
            },
          );
        },
      }),
    open: (key) => { setActiveKey(key); queueMicrotask(() => accordion.open(key)); },
    toggle: (key) => { setActiveKey(activeKey() === key ? null : key); accordion.toggle(key); },
  };
}
