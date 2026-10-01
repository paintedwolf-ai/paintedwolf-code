import { prefersReducedMotion } from "../interaction/reduced-motion.ts";

const BOOT_FADE_MS = 480;

function markAppReady(): void {
  document.body.classList.add("den-app-ready");
}

function bootRevealComplete(el: HTMLElement): boolean {
  const opacity = Number.parseFloat(getComputedStyle(el).opacity);
  return Number.isFinite(opacity) && opacity >= 0.99;
}

function waitForBootReveal(el: HTMLElement): Promise<void> {
  if (bootRevealComplete(el)) return Promise.resolve();

  return new Promise((resolve) => {
    let settled = false;
    const done = () => {
      if (settled) return;
      settled = true;
      resolve();
    };

    el.addEventListener(
      "animationend",
      (event) => {
        if (event.animationName === "den-boot-fade-in") done();
      },
      { once: true },
    );
    window.setTimeout(done, BOOT_FADE_MS + 48);
  });
}

export async function dismissBootFallback(options?: {
  immediate?: boolean;
}): Promise<void> {
  const el = document.getElementById("den-boot-fallback");
  if (!el) {
    markAppReady();
    return;
  }

  if (options?.immediate || prefersReducedMotion()) {
    el.remove();
    markAppReady();
    return;
  }

  await waitForBootReveal(el);

  await new Promise<void>((resolve) => {
    const finish = () => {
      el.remove();
      resolve();
    };
    el.addEventListener(
      "animationend",
      (event) => {
        if (event.animationName === "den-boot-fade-out") finish();
      },
      { once: true },
    );
    window.setTimeout(finish, BOOT_FADE_MS + 48);

    // Run first layout under the fading splash.
    markAppReady();
    el.style.pointerEvents = "none";
    el.classList.add("den-boot-fallback--out");
  });
}
