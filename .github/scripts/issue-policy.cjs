const exemptUsers = new Set([
  "chris-beckman",
  ...(process.env.ISSUE_AUTOMATION_EXEMPT_USERS || "").split(/[\s,]+/),
].map((login) => login.trim().replace(/^@/, "").toLowerCase()).filter(Boolean));

const isExempt = (issue) => exemptUsers.has(issue.user?.login?.toLowerCase());

async function markStale({ github, context, core }) {
  const debugOnly = process.env.DEBUG_ONLY === "true";
  const cutoff = Date.now() - 90 * 86400000;
  const exemptLabels = new Set(["never-stale", "accepting-work", "security"]);
  let writes = 0;
  const issues = await github.paginate(github.rest.issues.listForRepo, {
    ...context.repo, state: "open", sort: "updated", direction: "asc", per_page: 100,
  });
  for (const issue of issues) {
    if (issue.pull_request || issue.locked || isExempt(issue)) continue;
    if (issue.assignee || issue.assignees?.length) continue;
    const labels = issue.labels.map((label) => typeof label === "string" ? label : label.name);
    if (labels.some((label) => exemptLabels.has(label))) continue;
    const args = { ...context.repo, issue_number: issue.number };
    if (labels.includes("stale")) {
      const events = await github.paginate(github.rest.issues.listEvents, { ...args, per_page: 100 });
      const labeledAt = events.filter((event) => event.event === "labeled" && event.label?.name === "stale")
        .map((event) => event.created_at).pop();
      if (!labeledAt) continue;
      const comments = await github.paginate(github.rest.issues.listComments, {
        ...args, since: labeledAt, per_page: 100,
      });
      const replied = comments.some((comment) => comment.user?.type !== "Bot" &&
        Date.parse(comment.created_at) > Date.parse(labeledAt));
      if (!replied) continue;
      core.info(`${debugOnly ? "[dry-run] " : ""}unstale #${issue.number}`);
      if (!debugOnly) {
        await github.rest.issues.addLabels({ ...args, labels: ["never-stale"] });
        await github.rest.issues.removeLabel({ ...args, name: "stale" });
      }
    } else {
      if (Date.parse(issue.updated_at) >= cutoff) continue;
      core.info(`${debugOnly ? "[dry-run] " : ""}stale #${issue.number}`);
      if (!debugOnly) {
        await github.rest.issues.addLabels({ ...args, labels: ["stale"] });
        await github.rest.issues.createComment({
          ...args,
          body: "This has had no activity for 90 days, so it is being marked stale to keep the " +
            "tracker honest about what is actually being worked on. If it still matters, a " +
            'comment removes the label — including just "still happening on <version>". ' +
            "If it stopped happening, feel free to close it yourself.",
        });
      }
    }
    writes += 2;
    if (writes >= 300) { core.info("write cap reached; remainder next run"); return; }
  }
}

module.exports = { isExempt, markStale };
