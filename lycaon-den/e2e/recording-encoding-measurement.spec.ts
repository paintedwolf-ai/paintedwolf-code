import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { gzipSync } from "node:zlib";
import type { LiveToolRecording } from "../src/chat/visual/live-tool-recording.ts";

type MeasurementWindow = Window & {
  fixtureRecording?: LiveToolRecording | null;
  fixtureResult?: { bytes: number[]; durationMs: number; mime: string };
  fixtureRaw?: Promise<ArrayBuffer>;
  fixtureDrawTimes?: number[];
};
const outputDir = process.env.PW_RECORDING_MEASURE_DIR;
const seconds = 20;
const fixture = `<!doctype html><html><head><style>
*{box-sizing:border-box}body{margin:0;background:#f3f5f8;color:#243044;font:16px Arial,sans-serif}
header{height:70px;background:#203854;color:white;padding:22px 30px;font-weight:bold;font-size:24px}
main{padding:24px 30px}h1{margin:0 0 12px;font-size:24px}p{line-height:1.5;margin:0 0 16px}
.editor{background:white;border:1px solid #bdc8d4;padding:16px;height:90px;font:16px monospace;white-space:pre-wrap}
#items{margin-top:20px;height:248px;overflow:hidden;background:white;border:1px solid #c4ced8}.row{padding:14px 16px;height:58px;border-bottom:1px solid #dbe1e8}.row b{display:inline-block;width:105px}.row:nth-child(even){background:#f7f9fb}
</style></head><body><header>Project workspace</header><main><h1>Release review</h1><p>Review changes, check results, and prepare the next version.</p><div class="editor" id="editor">Notes for the next release:</div><div id="items">${Array.from({ length: 50 }, (_, index) => `<div class="row"><b>Change ${index + 1}</b> Update project documentation and verify behavior.</div>`).join("")}</div></main></body></html>`;

function inspect(file: string) {
  return JSON.parse(execFileSync("ffprobe", ["-v", "error", "-select_streams", "v:0", "-show_streams", "-show_format", "-show_frames", "-show_entries", "stream=codec_name,profile,width,height,r_frame_rate,avg_frame_rate,time_base,duration,nb_frames:format=duration,size:frame=pts_time,best_effort_timestamp_time,pkt_duration_time", "-of", "json", file], { maxBuffer: 16 * 1024 * 1024 }).toString()) as {
    streams: Record<string, unknown>[]; format: { duration: string; size: string };
    frames: { pts_time?: string; best_effort_timestamp_time?: string; pkt_duration_time?: string }[];
  };
}
function decodedHashes(file: string) {
  return execFileSync("ffmpeg", ["-v", "error", "-i", file, "-map", "0:v:0", "-vsync", "0", "-f", "framemd5", "-"], { maxBuffer: 16 * 1024 * 1024 }).toString().split("\n")
    .filter((line) => line && !line.startsWith("#")).map((line) => {
      const fields = line.split(",");
      const hash = fields[fields.length - 1]?.trim();
      if (!hash) throw new Error("Decoded frame checksum is missing");
      return hash;
    });
}
function packetHashes(file: string) {
  const value = JSON.parse(execFileSync("ffprobe", ["-v", "error", "-select_streams", "v:0", "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=data_hash", "-of", "json", file]).toString()) as { packets: { data_hash: string }[] };
  return value.packets.map((packet) => packet.data_hash);
}
async function playback(page: Page, bytes: Buffer, seekDuration?: number) {
  return page.evaluate(async ({ encoded, seekDuration }) => {
    const binary = atob(encoded);
    const data = Uint8Array.from(binary, (character) => character.charCodeAt(0));
    const url = URL.createObjectURL(new Blob([data], { type: "video/mp4" }));
    const video = document.createElement("video");
    video.muted = true; video.playsInline = true; video.style.cssText = "width:360px;height:270px";
    document.body.append(video);
    const loaded = new Promise<void>((resolve, reject) => { video.onloadedmetadata = () => resolve(); video.onerror = () => reject(new Error(`video error ${video.error?.code}`)); });
    video.src = url; await loaded;
    const duration = video.duration;
    await video.play();
    await new Promise<void>((resolve) => { video.ontimeupdate = () => { if (video.currentTime > 0.15) resolve(); }; });
    video.pause();
    const seeks = [];
    for (const fraction of [0.1, 0.5, 0.9]) {
      const requested = (seekDuration ?? duration) * fraction;
      const sought = new Promise<void>((resolve) => { video.onseeked = () => resolve(); });
      video.currentTime = requested; await sought;
      const canvas = document.createElement("canvas"); canvas.width = video.videoWidth; canvas.height = video.videoHeight;
      const ctx = canvas.getContext("2d");
      if (!ctx) throw new Error("Recording playback canvas is unavailable");
      ctx.drawImage(video, 0, 0);
      const pixels = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
      const digest = await crypto.subtle.digest("SHA-256", pixels);
      seeks.push({ requested, actual: video.currentTime, pixelHash: Array.from(new Uint8Array(digest), (value) => value.toString(16).padStart(2, "0")).join("") });
    }
    video.remove(); URL.revokeObjectURL(url);
    return { duration, seeks };
  }, { encoded: bytes.toString("base64"), seekDuration });
}

test("recording encoding measurement preserves decoded frames and seeking", async ({ page, browserName, browser }) => {
  if (!outputDir) {
    test.skip(true, "Set PW_RECORDING_MEASURE_DIR for the explicit recording measurement.");
    return;
  }
  test.setTimeout(240_000);
  mkdirSync(outputDir, { recursive: true });
  await page.setViewportSize({ width: 720, height: 540 });
  await page.route("**/recording-measurement-fixture", (route) => route.fulfill({ contentType: "text/html", body: fixture }));
  const measurements = [];
  for (const scene of ["static", "typing", "scroll"] as const) {
    await page.goto("/recording-measurement-fixture");
    const encoder = await page.evaluate(async () => {
      const modulePath = "/src/chat/visual/live-tool-recording.ts";
      const module: typeof import("../src/chat/visual/live-tool-recording.ts") = await import(modulePath);
      const win = window as unknown as MeasurementWindow;
      const Recorder = window.MediaRecorder;
      window.MediaRecorder = class extends Recorder {
        constructor(stream: MediaStream, options?: MediaRecorderOptions) {
          super(stream, options);
          const chunks: BlobPart[] = [];
          this.addEventListener("dataavailable", (event) => chunks.push(event.data));
          this.addEventListener("stop", () => { win.fixtureRaw = new Blob(chunks, { type: "video/mp4" }).arrayBuffer(); });
        }
      };
      const draw = CanvasRenderingContext2D.prototype.drawImage;
      const drawTimes: number[] = [];
      win.fixtureDrawTimes = drawTimes;
      CanvasRenderingContext2D.prototype.drawImage = function (this: CanvasRenderingContext2D, ...args: unknown[]) {
        drawTimes.push(performance.now());
        return Reflect.apply(draw, this, args);
      };
      win.fixtureRecording = module.startLiveToolRecording((result: { blob: Blob; durationMs: number }) => result.blob.arrayBuffer().then((buffer) => {
        win.fixtureResult = { bytes: Array.from(new Uint8Array(buffer)), durationMs: result.durationMs, mime: result.blob.type };
      }));
      return { supported: !!win.fixtureRecording, mime: module.LIVE_TOOL_RECORDING_MIME, captureFps: module.LIVE_TOOL_RECORDING_CAPTURE_FPS, ingestionFps: module.LIVE_TOOL_RECORDING_FPS, userAgent: navigator.userAgent, visibility: document.visibilityState, focused: document.hasFocus() };
    });
    expect(encoder.supported).toBe(true);
    const began = Date.now();
    for (let index = 0; index < seconds; index++) {
      const delay = began + index * 1000 - Date.now();
      if (delay > 0) await new Promise((resolve) => setTimeout(resolve, delay));
      await page.evaluate(({ scene, index }) => {
        if (scene === "typing") {
          const editor = document.getElementById("editor");
          if (!editor) throw new Error("Recording measurement editor is missing");
          editor.textContent = "Notes for the next release:\n" + "Check the migration, restore the backup, and review the update.".slice(0, index * 3);
        }
        if (scene === "scroll") {
          const items = document.getElementById("items");
          if (!items) throw new Error("Recording measurement list is missing");
          items.scrollTop = index * 37;
        }
      }, { scene, index });
      const jpeg = await page.screenshot({ type: "jpeg", quality: 80 });
      await page.evaluate((jpegB64) => {
        const recording = (window as unknown as MeasurementWindow).fixtureRecording;
        if (!recording) throw new Error("Recording measurement recorder is unavailable");
        recording.pushFrame({ jpegB64, mime: "image/jpeg", width: 720, height: 540 });
      }, jpeg.toString("base64"));
    }
    const finalDelay = began + seconds * 1000 - Date.now();
    if (finalDelay > 0) await new Promise((resolve) => setTimeout(resolve, finalDelay));
    const result = await page.evaluate(async () => {
      const win = window as unknown as MeasurementWindow;
      if (!win.fixtureRecording) throw new Error("Recording measurement recorder is unavailable");
      await win.fixtureRecording.stop();
      if (!win.fixtureResult) throw new Error("Recording measurement produced no result");
      if (!win.fixtureRaw) throw new Error("Raw recording chunks are unavailable");
      return { ...win.fixtureResult, raw: Array.from(new Uint8Array(await win.fixtureRaw)), drawTimes: win.fixtureDrawTimes ?? [] };
    });
    expect(result.bytes.length).toBeGreaterThan(0);
    const original = path.join(outputDir, `${scene}-original.mp4`);
    const candidate = path.join(outputDir, `${scene}-stream-copy.mp4`);
    writeFileSync(original, Buffer.from(result.bytes));
    const raw = path.join(outputDir, `${scene}-raw.mp4`);
    writeFileSync(raw, Buffer.from(result.raw));
    execFileSync("ffmpeg", ["-v", "error", "-y", "-copyts", "-i", original, "-map", "0:v:0", "-c:v", "copy", "-copytb", "1", "-map_metadata", "-1", "-movflags", "+faststart", candidate]);
    const originalInfo = inspect(original); const candidateInfo = inspect(candidate); const rawInfo = inspect(raw);
    const originalFrames = decodedHashes(original); const candidateFrames = decodedHashes(candidate);
    const sameFrames = JSON.stringify(originalFrames) === JSON.stringify(candidateFrames);
    const samePacketPayloads = JSON.stringify(packetHashes(original)) === JSON.stringify(packetHashes(candidate));
    const frameTimes = (info: ReturnType<typeof inspect>) => info.frames.map((frame) => frame.best_effort_timestamp_time ?? frame.pts_time);
    const sameFrameTimes = JSON.stringify(frameTimes(originalInfo)) === JSON.stringify(frameTimes(candidateInfo));
    const originalPlayback = await playback(page, readFileSync(original));
    const rawPlayback = await playback(page, readFileSync(raw));
    const candidatePlayback = await playback(page, readFileSync(candidate));
    const elapsedSeconds = result.durationMs / 1000;
    const elapsedOriginal = await playback(page, readFileSync(original), elapsedSeconds);
    const elapsedRaw = await playback(page, readFileSync(raw), elapsedSeconds);
    const elapsedCandidate = await playback(page, readFileSync(candidate), elapsedSeconds);
    const seekHashes = (value: Awaited<ReturnType<typeof playback>>) => value.seeks.map((seek) => seek.pixelHash);
    const sameElapsedSeekFrames = JSON.stringify(seekHashes(elapsedOriginal)) === JSON.stringify(seekHashes(elapsedRaw))
      && JSON.stringify(seekHashes(elapsedRaw)) === JSON.stringify(seekHashes(elapsedCandidate));
    const sameSeekFrames = originalPlayback.seeks.length === candidatePlayback.seeks.length
      && originalPlayback.seeks.every((seek, index) => seek.pixelHash === candidatePlayback.seeks[index]?.pixelHash);
    const measured = { scene, browserName, browserVersion: browser.version(), encoder, requestedSeconds: seconds, recordedDurationMs: result.durationMs,
      originalBytes: statSync(original).size, candidateBytes: statSync(candidate).size, gzipOriginalBytes: gzipSync(readFileSync(original), { level: 9 }).length, rawBytesUnchanged: Buffer.from(result.bytes).equals(Buffer.from(result.raw)),
      elapsedOriginal, elapsedRaw, elapsedCandidate, sameElapsedSeekFrames, rawPlayback, rawInfo, rawSamePacketPayloads: JSON.stringify(packetHashes(raw)) === JSON.stringify(packetHashes(original)), rawSameFrameTimes: JSON.stringify(frameTimes(rawInfo)) === JSON.stringify(frameTimes(originalInfo)), drawTimes: result.drawTimes, rawSameFrames: JSON.stringify(decodedHashes(raw)) === JSON.stringify(originalFrames), originalInfo, candidateInfo, decodedFrameCount: originalFrames.length, sameFrames, samePacketPayloads, sameFrameTimes, sameSeekFrames, originalPlayback, candidatePlayback };
    measurements.push(measured);
    writeFileSync(path.join(outputDir, "measurement.json"), JSON.stringify(measurements, null, 2));
  }
  for (const measured of measurements) {
    expect(measured.sameFrames, measured.scene).toBe(true);
    expect(measured.samePacketPayloads, measured.scene).toBe(true);
    expect(measured.rawBytesUnchanged, measured.scene).toBe(true);
    expect(measured.originalPlayback.duration, measured.scene).toBeCloseTo(measured.rawPlayback.duration, 6);
    expect(measured.rawSameFrames, measured.scene).toBe(true);
    expect(measured.rawSamePacketPayloads, measured.scene).toBe(true);
    expect(measured.rawSameFrameTimes, measured.scene).toBe(true);
    expect(measured.sameElapsedSeekFrames, measured.scene).toBe(true);
  }
});
