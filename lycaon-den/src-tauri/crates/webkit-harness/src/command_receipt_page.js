// Page-side driver for the real macOS WKWebView command activity + output-specific receipt identity + reload smoke.
window.__wkReceiptSmoke = {
  async waitHarness() {
    for (let i = 0; i < 120 && !window.__harness; i++) {
      await new Promise((r) => setTimeout(r, 250));
    }
    if (!window.__harness) throw new Error("window.__harness missing in WKWebView");
  },

  async api(token, method, path, body) {
    const headers = { Authorization: `Bearer ${token}` };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const resp = await fetch(path, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const text = await resp.text();
    if (!resp.ok) throw new Error(`${method} ${path} -> ${resp.status}: ${text}`);
    return text ? JSON.parse(text) : null;
  },

  async invokeTool(token, sessionId, name, args) {
    const pending = await this.api(token, "GET", `/harness/llm/pending?wait=30000&session_id=${encodeURIComponent(sessionId)}`);
    if (!pending?.pending) throw new Error(`no pending LLM turn for ${name}`);
    const id = crypto.randomUUID();
    await this.api(token, "POST", "/harness/llm/respond", {
      id: pending.id,
      content: "",
      tool_calls: [{ id, name, args }],
    });
    return id;
  },

  async waitToolCompleted(token, sessionId, callId) {
    for (let i = 0; i < 160; i++) {
      const body = await this.api(token, "GET", `/v1/sessions/${encodeURIComponent(sessionId)}/messages`);
      const found = (body?.messages || []).find((m) => m.tool_result?.tool_call_id === callId)?.tool_result;
      if (found) {
        if (found.outcome !== "completed") {
          throw new Error(`tool ${callId} outcome=${found.outcome}: ${found.content}`);
        }
        return found;
      }
      await new Promise((r) => setTimeout(r, 250));
    }
    throw new Error(`timed out waiting for tool_result ${callId}`);
  },

  expandIfCollapsed(container) {
    if (!container) return false;
    if (container.getAttribute("data-expanded") !== "true") {
      const hdr = container.querySelector(":scope > .den-transcript-disclosure-header");
      if (hdr) {
        hdr.click();
        return true;
      }
    }
    return false;
  },

  parseCommandStdout(toolResult) {
    const raw = String(toolResult?.content || "");
    const nl = raw.indexOf("\n");
    const jsonPart = nl >= 0 ? raw.slice(nl + 1).trim() : raw.trim();
    try {
      const parsed = JSON.parse(jsonPart);
      if (typeof parsed?.stdout === "string") return parsed.stdout;
    } catch (_err) {}
    return "";
  },

  checkCardOutputSurface(card, uniqueMarker) {
    const preview = card.querySelector('[data-testid="action-preview"][data-kind="output"]');
    if (!preview) return { found: false, text: "" };
    const text = preview.textContent || "";
    return { found: text.includes(uniqueMarker), text };
  },

  async inspectRendered(token, sessionId, expectedCallId, expectedProgramId, uniqueMarker, commandSource, maxAttempts = 80) {
    for (let i = 0; i < maxAttempts; i++) {
      const stream = document.querySelector(`[data-testid="chat-stream"][data-session-id="${CSS.escape(sessionId)}"]`);
      if (stream) {
        for (const span of stream.querySelectorAll('[data-testid="activity-span-card"]')) {
          this.expandIfCollapsed(span);
        }
        const card = stream.querySelector(`[data-testid="action-card"][data-tool-call-id="${CSS.escape(expectedCallId)}"]`);
        if (card) {
          this.expandIfCollapsed(card);
          const renderedCallId = card.getAttribute("data-tool-call-id") || "";
          const renderedStatus = card.getAttribute("data-status") || "";
          const outCheck = this.checkCardOutputSurface(card, uniqueMarker);
          if (renderedCallId === expectedCallId && outCheck.found) {
            const body = await this.api(token, "GET", `/v1/sessions/${encodeURIComponent(sessionId)}/messages`);
            const tr = (body?.messages || []).find((m) => m.tool_result?.tool_call_id === expectedCallId)?.tool_result;
            const pid = tr?.command_program?.id || "";
            const state = tr?.command_program?.state || "";
            const sidecarStdout = this.parseCommandStdout(tr);
            const sidecarStdoutHasMarker = sidecarStdout.includes(uniqueMarker);
            const markerAbsentFromSource = !String(commandSource || "").includes(uniqueMarker);
            const exactMatch = Boolean(
              tr
              && tr.tool_call_id === expectedCallId
              && pid.length > 0
              && (!expectedProgramId || pid === expectedProgramId)
              && state === "completed"
              && sidecarStdoutHasMarker
              && markerAbsentFromSource
            );
            // Negative control: remove the output preview node while leaving the command card present;
            // output-specific check MUST fail, then restore.
            const preview = card.querySelector('[data-testid="action-preview"][data-kind="output"]');
            const parent = preview.parentNode;
            const nextSib = preview.nextSibling;
            parent.removeChild(preview);
            const cardStillPresent = Boolean(stream.querySelector(`[data-testid="action-card"][data-tool-call-id="${CSS.escape(expectedCallId)}"]`));
            const afterRemoveCheck = this.checkCardOutputSurface(card, uniqueMarker);
            parent.insertBefore(preview, nextSib);
            const outputRemovedControlRejected = Boolean(cardStillPresent && !afterRemoveCheck.found);

            return {
              sessionId,
              toolCallId: expectedCallId,
              renderedCallId,
              renderedStatus,
              programId: pid,
              commandState: state,
              uniqueMarker,
              markerAbsentFromSource,
              outputPreviewHasUniqueOutput: outCheck.found,
              sidecarStdoutHasUniqueOutput: sidecarStdoutHasMarker,
              outputRemovedControlRejected,
              receiptIdentityMatched: exactMatch,
              domHasAssistantReply: (stream.textContent || "").includes("WKWebView receipt verified."),
              userAgent: navigator.userAgent,
            };
          }
        }
      }
      await new Promise((r) => setTimeout(r, 250));
    }
    throw new Error(`exact action-card [data-tool-call-id="${expectedCallId}"] output preview [data-testid="action-preview"][data-kind="output"] with "${uniqueMarker}" not rendered`);
  },

  async verifyWrongCallRejected(token, sessionId, expectedProgramId, uniqueMarker, commandSource) {
    const wrongCallId = `wrong-${crypto.randomUUID()}`;
    try {
      await this.inspectRendered(token, sessionId, wrongCallId, expectedProgramId, uniqueMarker, commandSource, 4);
      return false;
    } catch (_err) {
      return true;
    }
  },

  async runBeforeReload(token) {
    this.step = "waitHarness";
    await this.waitHarness();
    this.step = "openProject";
    const proj = await window.__harness.openProject("Harness");
    if (proj?.ok !== true) throw new Error(`openProject failed: ${JSON.stringify(proj)}`);
    this.step = "newSession";
    const sess = await window.__harness.newSession();
    if (sess?.ok !== true) throw new Error(`newSession failed: ${JSON.stringify(sess)}`);
    this.step = "readSessionId";
    let sessionId = "";
    for (let i = 0; i < 60 && !sessionId; i++) {
      const el = document.querySelector('[data-testid="chat-stream"][data-session-id]');
      sessionId = el?.getAttribute("data-session-id") || "";
      if (!sessionId) await new Promise((r) => setTimeout(r, 200));
    }
    if (!sessionId) throw new Error("chat-stream session_id did not mount");

    // Generate runtime marker from two separate printf arguments so uniqueMarker NEVER appears verbatim in command source.
    const left = "wkwebview-out-";
    const right = crypto.randomUUID().slice(0, 12);
    const uniqueMarker = left + right;
    const commandSource = `printf '%s%s\\n' '${left}' '${right}'`;
    if (commandSource.includes(uniqueMarker)) throw new Error("marker unexpectedly appeared verbatim in command source");

    this.step = "disableAutoAndPostPrompt";
    await this.api(token, "POST", "/harness/llm/auto", { enabled: false });
    await this.api(token, "POST", `/v1/sessions/${encodeURIComponent(sessionId)}/prompts`, {
      operation_id: crypto.randomUUID(),
      text: "Run WKWebView command output-specific receipt smoke and verify reload.",
    });

    this.step = "requestUpdateProgress";
    const reqId = await this.invokeTool(token, sessionId, "request_tools", { need: "update_progress" });
    await this.waitToolCompleted(token, sessionId, reqId);
    this.step = "updateProgressOpen";
    const progCallId = await this.invokeTool(token, sessionId, "update_progress", {
      content: "## Progress\n- [ ] Verify WKWebView output-specific receipt and reload\n",
    });
    const progRes = await this.waitToolCompleted(token, sessionId, progCallId);
    const snap = JSON.parse(progRes.content).snapshot;

    this.step = "invokeCommand";
    const cmdCallId = await this.invokeTool(token, sessionId, "command", { command: commandSource });
    const cmdRes = await this.waitToolCompleted(token, sessionId, cmdCallId);
    const programId = cmdRes?.command_program?.id || "";
    if (!programId) throw new Error(`missing command_program.id in tool_result: ${JSON.stringify(cmdRes)}`);

    this.step = "updateProgressClose";
    const doneProgId = await this.invokeTool(token, sessionId, "update_progress", {
      content: "## Progress\n- [x] Verify WKWebView output-specific receipt and reload\n",
      expected_revision: snap.revision,
      item_ids: snap.item_ids,
    });
    await this.waitToolCompleted(token, sessionId, doneProgId);

    this.step = "finalAssistantReply";
    const finalPending = await this.api(token, "GET", `/harness/llm/pending?wait=30000&session_id=${encodeURIComponent(sessionId)}`);
    await this.api(token, "POST", "/harness/llm/respond", {
      id: finalPending.id,
      content: "WKWebView receipt verified.",
      tool_calls: [],
    });

    this.step = "inspectRenderedBeforeReload";
    const snapBefore = await this.inspectRendered(token, sessionId, cmdCallId, programId, uniqueMarker, commandSource);
    this.step = "wrongCallControlBeforeReload";
    snapBefore.wrongCallControlRejected = await this.verifyWrongCallRejected(token, sessionId, programId, uniqueMarker, commandSource);
    snapBefore.commandSource = commandSource;
    return snapBefore;
  },

  async runAfterReload(token, sessionId, expectedCallId, expectedProgramId, uniqueMarker, commandSource) {
    this.step = "afterReload:waitHarness";
    await this.waitHarness();
    this.step = "afterReload:openProject";
    const proj = await window.__harness.openProject("Harness");
    if (proj?.ok !== true) throw new Error(`openProject after reload failed: ${JSON.stringify(proj)}`);
    this.step = "afterReload:selectSession";
    if (typeof window.__harness.openSession === "function") {
      await window.__harness.openSession(sessionId);
    } else {
      const row = document.querySelector(`[data-session-id="${CSS.escape(sessionId)}"]`);
      if (row) row.click();
    }
    this.step = "afterReload:inspectRendered";
    const snapAfter = await this.inspectRendered(token, sessionId, expectedCallId, expectedProgramId, uniqueMarker, commandSource);
    this.step = "afterReload:wrongCallControl";
    snapAfter.wrongCallControlRejected = await this.verifyWrongCallRejected(token, sessionId, expectedProgramId, uniqueMarker, commandSource);
    snapAfter.commandSource = commandSource;
    return snapAfter;
  },
};
