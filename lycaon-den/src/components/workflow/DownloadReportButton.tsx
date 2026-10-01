import { createSignal, Show } from "solid-js";
import { downloadExport } from "../../platform/files/save-file.ts";
import { DenButton, type DenButtonVariant } from "../primitives/DenButton.tsx";

export type ReportDownloadState = "idle" | "generating" | "ready" | "error";

type Props = {
  /** Workflow run associated with the report. */
  runId: string;
  downloadReport: (runId: string) => Promise<{ blob: Blob; filename: string }>;
  variant?: "panel" | "transcript";
  disabled?: boolean;
};

function labelFor(state: ReportDownloadState): string {
  switch (state) {
    case "generating":
      return "Downloading…";
    case "error":
      return "Retry download";
    case "ready":
    case "idle":
      return "Download report";
  }
}

function denVariant(variant: "panel" | "transcript"): DenButtonVariant {
  switch (variant) {
    case "transcript":
      return "link";
    case "panel":
      return "secondary";
  }
}

export function DownloadReportButton(props: Props) {
  const [state, setState] = createSignal<ReportDownloadState>("idle");

  const onClick = async () => {
    if (state() === "generating") return;
    setState("generating");
    try {
      const { blob, filename } = await props.downloadReport(props.runId);
      await downloadExport(blob, filename);
      setState("ready");
    } catch {
      setState("error");
    }
  };

  const variant = () => props.variant ?? "panel";

  return (
    <span class="den-report-download" data-state={state()}>
      <DenButton
        variant={denVariant(variant())}
        compact={variant() !== "transcript"}
        class={variant() === "transcript" ? "den-report-download__transcript" : undefined}
        disabled={props.disabled || state() === "generating"}
        data-testid="workflow-download-report"
        data-variant={variant()}
        data-state={state()}
        onClick={() => void onClick()}
      >
        {labelFor(state())}
      </DenButton>
      <Show when={state() === "error"}>
        <span class="den-report-download__error" role="alert" data-testid="workflow-download-report-error">
          Couldn’t download report
        </span>
      </Show>
    </span>
  );
}
