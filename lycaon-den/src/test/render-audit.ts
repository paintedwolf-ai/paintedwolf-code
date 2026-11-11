/** One problem found in a rendered document. */
export type RenderAuditFinding = {
  check: "unnamed_control" | "broken_text" | "engine_name" | "secret_exposed";
  detail: string;
};

const CONTROLS =
  "button, a[href], input:not([type=hidden]), select, textarea, summary, " +
  "[role=button], [role=link], [role=menuitem], [role=tab], [role=checkbox], " +
  "[role=radio], [role=switch], [role=option], [role=combobox], [role=textbox]";

const BROKEN_TEXT = /\bundefined\b|\bNaN\b|\[object Object\]|\{\{|\{%/;
const ENGINE_NAME = /\blycaon\b/i;
const NAMING_ATTRIBUTES = ["aria-label", "title", "placeholder", "alt"];

const secrets = new Set<string>();

/** Declares a fixture value that must never reach a rendered document. */
export function markSecret<T extends string>(value: T): T {
  secrets.add(value);
  return value;
}

/** Forgets declared secrets so one test's fixtures do not constrain the next. */
export function clearMarkedSecrets(): void {
  secrets.clear();
}

function isHidden(element: Element): boolean {
  return element.closest("[hidden], [aria-hidden=true], [inert]") !== null;
}

function excerpt(element: Element): string {
  return element.outerHTML.slice(0, 120);
}

function hasAccessibleName(control: Element): boolean {
  if ((control.textContent ?? "").trim() !== "") return true;
  for (const name of NAMING_ATTRIBUTES) {
    if ((control.getAttribute(name) ?? "").trim() !== "") return true;
  }
  const labelledBy = control.getAttribute("aria-labelledby");
  if (labelledBy) {
    const root = control.getRootNode() as ParentNode & { getElementById?: (id: string) => Element | null };
    for (const id of labelledBy.split(/\s+/)) {
      if ((root.getElementById?.(id)?.textContent ?? "").trim() !== "") return true;
    }
  }
  if (control.querySelector("img[alt]:not([alt='']), svg title, [aria-label]") !== null) return true;
  if (control.closest("label") !== null) return true;
  if ("labels" in control && ((control as HTMLInputElement).labels?.length ?? 0) > 0) return true;
  if (control instanceof HTMLInputElement && ["submit", "button", "reset"].includes(control.type)) {
    return control.value.trim() !== "";
  }
  return false;
}

/**
 * Checks a finished render for problems no single test looks for.
 *
 * Every check reads structure or values a fixture declared, never product
 * wording. Text is examined only once per document.
 */
export function auditRender(root: Element): RenderAuditFinding[] {
  const findings: RenderAuditFinding[] = [];
  for (const control of root.querySelectorAll(CONTROLS)) {
    if (!isHidden(control) && !hasAccessibleName(control)) {
      findings.push({ check: "unnamed_control", detail: excerpt(control) });
    }
  }

  const parts = [root.textContent ?? ""];
  for (const element of root.querySelectorAll(NAMING_ATTRIBUTES.map((name) => `[${name}]`).join(","))) {
    for (const name of NAMING_ATTRIBUTES) {
      const value = element.getAttribute(name);
      if (value) parts.push(value);
    }
  }
  const text = parts.join("\n");

  const broken = BROKEN_TEXT.exec(text);
  if (broken) {
    findings.push({ check: "broken_text", detail: context(text, broken.index) });
  }
  const engine = ENGINE_NAME.exec(text);
  if (engine) {
    findings.push({ check: "engine_name", detail: context(text, engine.index) });
  }
  for (const secret of secrets) {
    if (text.includes(secret)) {
      findings.push({ check: "secret_exposed", detail: "a marked fixture value reached the document" });
    }
  }
  return findings;
}

function context(text: string, at: number): string {
  return text.slice(Math.max(0, at - 40), at + 40).replace(/\s+/g, " ");
}
