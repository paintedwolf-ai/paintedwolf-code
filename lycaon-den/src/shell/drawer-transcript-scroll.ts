import { scrollportMotionContaining } from "../platform/scrolling/scrollport-motion.ts";

const SECTION_SUMMARY = ".den-worker-transcript-section-summary";
const SECTION_BODY = ".den-worker-transcript-section-body";

function sectionHeaderHeight(pane: HTMLElement): number {
  return (
    pane.querySelector<HTMLElement>(SECTION_SUMMARY)?.offsetHeight ?? 32
  );
}

export function pinDrawerTranscriptSection(args: {
  pane: HTMLElement | undefined;
  sectionKey: string;
  sectionIndex: number;
}): Promise<boolean> {
  const pane = args.pane;
  if (!pane || args.sectionIndex < 0) return Promise.resolve(false);
  const motion = scrollportMotionContaining(pane);
  const scroller = motion?.viewport;
  const body = pane.querySelector<HTMLElement>(
    `[data-section="${args.sectionKey}"] ${SECTION_BODY}`,
  );
  if (!scroller || !body) return Promise.resolve(false);
  const header = sectionHeaderHeight(pane);
  const bodyTop =
    body.getBoundingClientRect().top -
    scroller.getBoundingClientRect().top +
    scroller.scrollTop;
  const top = bodyTop - header;
  return motion.revealOffset(Math.max(0, top - args.sectionIndex * header));
}
