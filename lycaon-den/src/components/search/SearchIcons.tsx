import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { ICON_SLOTS, type IconSlot } from "../../contributions/theme-vocabulary.generated.ts";

export function SearchIcon() {
  return <ThemeIcon slot="search" size={14} />;
}

export function ProjectFolderIcon() {
  return <ThemeIcon slot="project-folder" size={12} />;
}

export function ActionCommandIcon(props: { icon: string }) {
  return <ThemeIcon slot={commandIconSlot(props.icon)} size={13} />;
}

function commandIconSlot(icon: string): IconSlot {
  return icon in ICON_SLOTS ? (icon as IconSlot) : "play";
}

export function GotoTargetIcon(props: {
  kind: "session" | "project" | "surface" | "setting";
}) {
  const slot = (): IconSlot =>
    props.kind === "project" ? "project-folder" : props.kind === "setting" ? "settings" : props.kind;
  return <ThemeIcon slot={slot()} size={13} />;
}

export function RecentQueryIcon() {
  return <ThemeIcon slot="recents" size={13} />;
}

export function EscalateArrowIcon() {
  return <ThemeIcon slot="escalate" size={13} />;
}

export function SearchKindIcon(props: { kind: string }) {
  return <ThemeIcon slot={kindSlot(props.kind.trim().toLowerCase())} size={13} />;
}

function kindSlot(kind: string): IconSlot {
  switch (kind) {
    case "code":
    case "symbol":
      return "code";
    case "file":
      return "file";
    case "message":
      return "session";
    case "evidence":
      return "evidence";
    case "claim":
      return "claim";
    case "tool":
      return "tool";
    case "web":
      return "external-content";
    case "finding":
      return "shield";
    case "artifact":
      return "artifact";
    case "outcome":
      return "outcome";
    case "network":
      return "network";
    default:
      return "impact";
  }
}

export function PaletteSlotIcon(props: { slot: IconSlot }) {
  return <ThemeIcon slot={props.slot} size={13} />;
}
