import {
  createSignal,
  onCleanup,
  onMount,
  type ComponentProps,
} from "solid-js";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
  type TranscriptViewportController,
} from "../../chat/stream/transcript-viewport.tsx";
import {
  bindScrollportMotion,
  unbindScrollportMotion,
} from "../../platform/scrolling/scrollport-motion.ts";
import { ChatSpanBlocks as ChatSpanBlocksView } from "./ChatSpanBlocks.tsx";
import { SessionTranscript as SessionTranscriptView } from "./SessionTranscript.tsx";

function TranscriptViewportTestBoundary(
  props: {
    viewport: TranscriptViewportController;
    render: () => ReturnType<typeof SessionTranscriptView>;
  },
) {
  let host!: HTMLDivElement;
  const [ready, setReady] = createSignal(false);
  onMount(() => {
    bindScrollportMotion(host, host, host);
    props.viewport.attachStream(host);
    setReady(true);
    onCleanup(() => {
      props.viewport.attachStream(null);
      unbindScrollportMotion(host);
    });
  });
  return (
    <TranscriptViewportProvider value={props.viewport}>
      <div class="den-chat-stream" ref={host}>
        {ready() ? props.render() : null}
      </div>
    </TranscriptViewportProvider>
  );
}

export function SessionTranscript(
  props: ComponentProps<typeof SessionTranscriptView>,
) {
  const viewport = createTranscriptViewportController({
    sessionId: () => props.sessionId?.trim() || "test-session",
  });
  return (
    <TranscriptViewportTestBoundary
      viewport={viewport}
      render={() => <SessionTranscriptView {...props} />}
    />
  );
}

export function ChatSpanBlocks(props: ComponentProps<typeof ChatSpanBlocksView>) {
  const viewport = createTranscriptViewportController({
    sessionId: () => props.sessionId.trim() || "test-session",
  });
  return (
    <TranscriptViewportTestBoundary
      viewport={viewport}
      render={() => <ChatSpanBlocksView {...props} />}
    />
  );
}
