import type { LivePreviewSnapshot } from "./preview-store.ts";

export const LIVE_TOOL_RECORDING_MIME = "video/mp4;codecs=avc1.42E01E";
const LIVE_TOOL_RECORDING_BLOB_MIME = "video/mp4";
const LIVE_TOOL_RECORDING_WIDTH = 720;
const LIVE_TOOL_RECORDING_HEIGHT = 540;
/** Preview frame ingestion rate. */
export const LIVE_TOOL_RECORDING_FPS = 1;
/** Canvas capture frame-rate request. */
export const LIVE_TOOL_RECORDING_CAPTURE_FPS = 10;
const LIVE_TOOL_RECORDING_BITS_PER_SECOND = 350_000;

const FRAME_INTERVAL_MS = 1000 / LIVE_TOOL_RECORDING_FPS;
const RECORDER_TIMESLICE_MS = 1_000;
export const LIVE_TOOL_RECORDING_MIN_FRAMES = 2;
export const LIVE_TOOL_RECORDING_MIN_DURATION_MS = FRAME_INTERVAL_MS;

export type CompletedLiveToolRecording = {
  blob: Blob;
  recordedAt: string;
  durationMs: number;
};

export type LiveToolRecordingArtifact = {
  sessionId: string;
  artifactId: string;
};

export type LiveToolRecordingFrame = {
  jpegB64: string;
  mime: string;
  width?: number;
  height?: number;
};

export type LiveToolRecording = {
  pushFrame(frame: LiveToolRecordingFrame): void;
  stop(): Promise<void>;
};

export type LiveToolRecordingEpisode = {
  sessionId: string;
  pageId: string;
  assistantMessageId: string;
  toolCallId: string;
};

type RecordingFactory = (
  episode: LiveToolRecordingEpisode,
) => LiveToolRecording | null;

export class LiveToolRecordingEpisodeEngine {
  private disposed = false;
  private pageKey = "";
  private lastFrameSeq: number | undefined;
  private recording: LiveToolRecording | null = null;

  constructor(private readonly startRecording: RecordingFactory) {}

  handleSnapshot(snapshot: LivePreviewSnapshot | undefined): void {
    if (this.disposed) return;
    const holderSessionId =
      snapshot?.holderSessionId?.trim() || snapshot?.sessionId.trim() || "";
    const pageId = snapshot?.pageId.trim() ?? "";
    const assistantMessageId = snapshot?.assistantMessageId.trim() ?? "";
    const toolCallId = snapshot?.toolCallId.trim() ?? "";
    const nextPageKey =
      snapshot?.live &&
      holderSessionId &&
      pageId &&
      assistantMessageId &&
      toolCallId
      ? `${holderSessionId}\u0000${pageId}\u0000${assistantMessageId}\u0000${toolCallId}`
      : "";

    if (nextPageKey !== this.pageKey) {
      this.stopCurrent();
      this.pageKey = nextPageKey;
      this.lastFrameSeq = undefined;
    }
    if (
      !nextPageKey ||
      snapshot?.frameSeq == null ||
      !snapshot.jpegB64 ||
      snapshot.frameSeq === this.lastFrameSeq
    ) {
      return;
    }

    this.lastFrameSeq = snapshot.frameSeq;
    this.recording ??= this.startRecording({
      sessionId: holderSessionId,
      pageId,
      assistantMessageId,
      toolCallId,
    });
    this.recording?.pushFrame({
      jpegB64: snapshot.jpegB64,
      mime: snapshot.mime?.trim() || "image/jpeg",
      width: snapshot.width,
      height: snapshot.height,
    });
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.pageKey = "";
    this.stopCurrent();
  }

  private stopCurrent(): void {
    const recording = this.recording;
    this.recording = null;
    void recording?.stop();
  }
}

export function preferredLiveToolRecordingMime(): string | null {
  if (typeof MediaRecorder === "undefined") return null;
  return MediaRecorder.isTypeSupported(LIVE_TOOL_RECORDING_MIME)
    ? LIVE_TOOL_RECORDING_MIME
    : null;
}

function frameSource(frame: LiveToolRecordingFrame): string {
  return `data:${frame.mime};base64,${frame.jpegB64}`;
}

function drawContained(
  context: CanvasRenderingContext2D,
  image: HTMLImageElement,
  sourceWidth: number,
  sourceHeight: number,
): void {
  const scale = Math.min(
    LIVE_TOOL_RECORDING_WIDTH / sourceWidth,
    LIVE_TOOL_RECORDING_HEIGHT / sourceHeight,
  );
  const width = Math.max(1, Math.round(sourceWidth * scale));
  const height = Math.max(1, Math.round(sourceHeight * scale));
  const x = Math.round((LIVE_TOOL_RECORDING_WIDTH - width) / 2);
  const y = Math.round((LIVE_TOOL_RECORDING_HEIGHT - height) / 2);
  context.fillStyle = "#111111";
  context.fillRect(
    0,
    0,
    LIVE_TOOL_RECORDING_WIDTH,
    LIVE_TOOL_RECORDING_HEIGHT,
  );
  context.drawImage(image, x, y, width, height);
}

export function startLiveToolRecording(
  onComplete: (recording: CompletedLiveToolRecording) => Promise<void> | void,
): LiveToolRecording | null {
  const mime = preferredLiveToolRecordingMime();
  if (!mime) return null;

  const canvas = document.createElement("canvas");
  canvas.width = LIVE_TOOL_RECORDING_WIDTH;
  canvas.height = LIVE_TOOL_RECORDING_HEIGHT;
  const context = canvas.getContext("2d");
  const stream = canvas.captureStream?.(LIVE_TOOL_RECORDING_CAPTURE_FPS);
  if (!context || !stream) return null;

  context.fillStyle = "#111111";
  context.fillRect(
    0,
    0,
    LIVE_TOOL_RECORDING_WIDTH,
    LIVE_TOOL_RECORDING_HEIGHT,
  );

  let recorder: MediaRecorder;
  try {
    recorder = new MediaRecorder(stream, {
      mimeType: mime,
      videoBitsPerSecond: LIVE_TOOL_RECORDING_BITS_PER_SECOND,
    });
  } catch {
    for (const track of stream.getTracks()) track.stop();
    return null;
  }

  const chunks: BlobPart[] = [];
  const recordedAt = new Date().toISOString();
  const startedAt = performance.now();
  let completedFrames = 0;
  let stopped = false;
  let drawing = false;
  let queuedFrame: LiveToolRecordingFrame | undefined;
  let frameTimer: number | undefined;
  let activeImage: HTMLImageElement | undefined;
  let lastFrameStartedAt = Number.NEGATIVE_INFINITY;
  let resolveStop: (() => void) | undefined;
  const stoppedPromise = new Promise<void>((resolve) => {
    resolveStop = resolve;
  });

  const scheduleFrame = () => {
    if (stopped || drawing || frameTimer != null || !queuedFrame) return;
    const wait = Math.max(
      0,
      FRAME_INTERVAL_MS - (performance.now() - lastFrameStartedAt),
    );
    frameTimer = window.setTimeout(() => {
      frameTimer = undefined;
      const frame = queuedFrame;
      queuedFrame = undefined;
      if (!frame || stopped) return;
      drawing = true;
      lastFrameStartedAt = performance.now();
      const image = new Image();
      activeImage = image;
      image.src = frameSource(frame);
      void image
        .decode()
        .then(() => {
          if (stopped || activeImage !== image) return;
          const sourceWidth = frame.width || image.naturalWidth;
          const sourceHeight = frame.height || image.naturalHeight;
          if (!sourceWidth || !sourceHeight) return;
          drawContained(context, image, sourceWidth, sourceHeight);
          completedFrames += 1;
        })
        .catch(() => undefined)
        .finally(() => {
          if (activeImage === image) activeImage = undefined;
          drawing = false;
          scheduleFrame();
        });
    }, wait);
  };

  recorder.ondataavailable = (event) => {
    if (event.data.size > 0) chunks.push(event.data);
  };
  recorder.onstop = () => {
    const durationMs = Math.max(0, Math.round(performance.now() - startedAt));
    void (async () => {
      try {
        if (
          completedFrames >= LIVE_TOOL_RECORDING_MIN_FRAMES &&
          chunks.length > 0
        ) {
          const blob = new Blob(chunks, { type: LIVE_TOOL_RECORDING_BLOB_MIME });
          if (blob.size > 0) {
            await Promise.resolve(onComplete({ blob, recordedAt, durationMs }));
          }
        }
      } finally {
        for (const track of stream.getTracks()) track.stop();
        resolveStop?.();
      }
    })();
  };
  try {
    recorder.start(RECORDER_TIMESLICE_MS);
  } catch {
    stopped = true;
    for (const track of stream.getTracks()) track.stop();
    resolveStop?.();
    return null;
  }

  return {
    pushFrame(frame) {
      if (stopped) return;
      queuedFrame = frame;
      scheduleFrame();
    },
    async stop() {
      if (stopped) return stoppedPromise;
      stopped = true;
      queuedFrame = undefined;
      if (frameTimer != null) window.clearTimeout(frameTimer);
      activeImage?.removeAttribute("src");
      activeImage = undefined;
      if (recorder.state !== "inactive") recorder.stop();
      return stoppedPromise;
    },
  };
}
