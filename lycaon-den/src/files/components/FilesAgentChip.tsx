import { agentChipClasses, type FilesAgentChip as Chip } from "./files-agent-presence.ts";

export function FilesAgentChip(props: { chip: Chip; testId: string; showTooltip?: boolean }) {
  return (
    <span
      class="den-files-presence"
      classList={agentChipClasses(props.chip.state)}
      data-testid={props.testId}
      data-agent-state={props.chip.state}
      aria-label={props.chip.description}
      data-tip={props.showTooltip === false ? undefined : props.chip.description}
    >
      {props.chip.text}
    </span>
  );
}
