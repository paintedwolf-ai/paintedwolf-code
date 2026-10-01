function consoleNoiseText(args: unknown[]): string {
  return args
    .map((arg) => (arg instanceof Error ? arg.message : String(arg)))
    .join(" ");
}

export function isAllowlistedConsoleNoise(args: unknown[]): boolean {
  const text = consoleNoiseText(args);
  // Dependency traversal warnings depend on host load.
  if (text.includes("[reactivity-store]")) {
    return true;
  }
  return false;
}
