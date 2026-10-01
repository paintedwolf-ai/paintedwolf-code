/** List ages, live elapsed readouts, and durations. */

/** Short relative age for list subtitles: just now, 5m ago, 3h ago, 2d ago. */
export function formatRelativeTime(timestampMs: number, now = Date.now()): string {
  const delta = Math.max(0, now - timestampMs);
  const sec = Math.floor(delta / 1000);
  if (sec < 60) return "just now";
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 48) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  if (day < 30) return `${day}d ago`;
  if (day < 365) return `${Math.floor(day / 30)}mo ago`;
  return `${Math.floor(day / 365)}y ago`;
}

/** Relative age of an RFC 3339 timestamp; empty when missing or unparseable. */
export function relativeTimeLabel(
  iso: string | null | undefined,
  now = Date.now(),
): string {
  const at = parseInstant(iso);
  return at === null ? "" : formatRelativeTime(at, now);
}

/** A live readout: `m:ss` under an hour, `h:mm:ss` past it. */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const seconds = total % 60;
  const minutes = Math.floor(total / 60) % 60;
  const hours = Math.floor(total / 3600);
  const mm = String(minutes).padStart(hours > 0 ? 2 : 1, "0");
  const ss = String(seconds).padStart(2, "0");
  return hours > 0 ? `${hours}:${mm}:${ss}` : `${mm}:${ss}`;
}

/** A settled duration in its two largest units: `45s`, `3m 24s`, `1h 5m`. */
export function formatDuration(ms: number): string {
  const totalSec = Math.max(0, Math.round(ms / 1000));
  const hours = Math.floor(totalSec / 3600);
  const minutes = Math.floor(totalSec / 60) % 60;
  const seconds = totalSec % 60;
  if (hours > 0) return `${hours}h ${minutes}m`;
  return minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`;
}

/** Epoch ms for a wire timestamp; null when absent or unparseable. */
export function parseInstant(value: string | null | undefined): number | null {
  if (!value?.trim()) return null;
  const at = Date.parse(value);
  return Number.isFinite(at) ? at : null;
}
