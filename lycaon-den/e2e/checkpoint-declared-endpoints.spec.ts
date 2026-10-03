import { expect, test } from "@playwright/test";
import {
  activateProject,
  apiConfig,
  apiFindHarnessProject,
  openNewChatSession,
  waitHarnessConnected,
  webE2e,
} from "./helpers.ts";

/** The configured-endpoint fan-out one web_search reaches. */
const HOSTS = [
  "api.crossref.org", "api.gdeltproject.org", "api.github.com", "api.openalex.org",
  "api.stackexchange.com", "archive.org", "azuresearch-usnc.nuget.org", "caniuse.com",
  "crates.io", "css-tricks.com", "datatracker.ietf.org", "developer.mozilla.org",
  "en.wikipedia.org", "en.wiktionary.org", "export.arxiv.org", "fastapi.metacpan.org",
  "forum.golangbridge.org", "gitlab.com", "hackage.haskell.org", "hex.pm",
  "hn.algolia.com", "huggingface.co", "jsr.io", "learn.microsoft.com",
  "openlibrary.org", "packagist.org", "pkg.go.dev", "pub.dev", "registry.npmjs.org",
  "rubygems.org", "search.maven.org", "search.r-pkg.org", "services.nvd.nist.gov",
  "users.rust-lang.org", "wiki.gentoo.org", "wiki.nixos.org", "www.ebi.ac.uk",
  "www.mankier.com", "www.wikidata.org",
];

/** Keep decision controls visible for the full destination set. */
webE2e(
  "den:harness — declared endpoint set card stays answerable",
  async ({ page, request }) => {
    test.setTimeout(180_000);

    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto("/");
    await waitHarnessConnected(page);

    const project = await apiFindHarnessProject(request);
    const { apiUrl, token } = apiConfig();
    const auth = { Authorization: `Bearer ${token}` };

    await activateProject(page, project.id);
    const sessionId = await openNewChatSession(page);

    const inject = await request.post(`${apiUrl}/harness/checkpoints/tool_approval`, {
      headers: { ...auth, "Content-Type": "application/json" },
      data: { session_id: sessionId, command: "web_search", declared_hosts: HOSTS },
    });
    expect(inject.ok(), `harness inject: ${await inject.text()}`).toBeTruthy();

    const card = page.getByTestId("tool-approval-card");
    await expect(card).toBeVisible({ timeout: 60_000 });

    await expect(card.getByTestId("approval-subject")).toHaveText(
      `Allow network: ${HOSTS.length} configured endpoints`,
    );
    const link = card.getByRole("button", {name:`Show all ${HOSTS.length} destinations in Files`,exact:true});
    await expect(link).toBeVisible();
    await expect(card.locator(".den-approval-card-scroll")).toHaveCount(1);
    await expect(card.getByTestId("approval-targets")).toHaveCount(0);
    await expect(async () => {
      const fits = await card.evaluate(el => {
        const inView = (selector: string) => {
          const node = el.querySelector(selector);
          if (!node) return false;
          const rect = node.getBoundingClientRect();
          return rect.top >= 0 && rect.bottom <= window.innerHeight && rect.height > 0;
        };
        return {height:el.getBoundingClientRect().height,viewport:window.innerHeight,
          primary:inView('[data-testid="approval-approve-primary"]'),
          no:inView('[data-testid="approval-no"]'),rail:inView('[data-testid="approval-redirect-rail"]')};
      });
      expect(fits.height).toBeLessThan(fits.viewport);
      expect(fits.primary).toBe(true);
      expect(fits.no).toBe(true);
      expect(fits.rail).toBe(true);
    }).toPass({timeout:5000});
    await link.press("Enter");
    const reader = page.getByTestId("chat-content-page");
    await expect(reader).toContainText(HOSTS[0]!);
    await reader.getByRole("button", {name:"Go to line",exact:true}).click();
    await page.getByTestId("goto-line-input").fill(String(HOSTS.length*2-1));
    await page.getByTestId("goto-line-input").press("Enter");
    await expect(reader).toContainText(HOSTS.at(-1)!);
  },
);
