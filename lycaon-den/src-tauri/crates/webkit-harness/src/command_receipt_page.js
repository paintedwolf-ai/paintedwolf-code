// Page-side driver for the real macOS WKWebView command activity + receipt identity + reload smoke.
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

  async inspectRendered(token, sessionId, expectedCallId, expectedProgramId) {
    for (let i = 0; i < 80; i++) {
      const stream = document.querySelector(`[data-testid="chat-stream"][data-session-id="${CSS.escape(sessionId)}"]`);
      const text = stream?.textContent || "";
      const hasReply = text.includes("WKWebView receipt verified.");
      const disclosures = stream ? stream.querySelectorAll(".den-transcript-disclosure, .den-transcript-disclosure-header, [data-tool-call-id], .activity-row") : [];
      if (hasReply && (disclosures.length > 0 || text.includes("wkwebview-proof.txt") || text.includes("command"))) {
        const body = await this.api(token, "GET", `/v1/sessions/${encodeURIComponent(sessionId)}/messages`);
        const tr = (body?.messages || []).find((m) => m.tool_result?.tool_call_id === expectedCallId)?.tool_result;
        const pid = tr?.command_program?.program_id || "";
        return {
          sessionId,
          toolCallId: tr?.tool_call_id || "",
          programId: pid,
          commandState: tr?.command_program?.state || "",
          receiptIdentityMatched: Boolean(tr && tr.tool_call_id === expectedCallId && (!expectedProgramId || pid === expectedProgramId)),
          domHasAssistantReply: hasReply,
          domDisclosureCount: disclosures.length,
          domHasCommandText: text.includes("wkwebview-proof.txt") || text.includes("wkwebview-receipt-ok") || disclosures.length > 0,
          userAgent: navigator.userAgent,
        };
      }
      await new Promise((r) => setTimeout(r, 250));
    }
    const stream = document.querySelector('[data-testid="chat-stream"]');
    throw new Error(`rendered DOM did not show command receipt for ${sessionId}; text=${(stream?.textContent || "").slice(0, 400)}`);
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

    this.step = "disableAutoAndPostPrompt";
    await this.api(token, "POST", "/harness/llm/auto", { enabled: false });
    await this.api(token, "POST", `/v1/sessions/${encodeURIComponent(sessionId)}/prompts`, {
      operation_id: crypto.randomUUID(),
      text: "Run WKWebView command receipt smoke and verify reload.",
    });

    this.step = "requestUpdateProgress";
    const reqId = await this.invokeTool(token, sessionId, "request_tools", { need: "update_progress" });
    await this.waitToolCompleted(token, sessionId, reqId);
    this.step = "updateProgressOpen";
    const progCallId = await this.invokeTool(token, sessionId, "update_progress", {
      content: "## Progress\n- [ ] Verify WKWebView receipt identity and reload\n",
    });
    const progRes = await this.waitToolCompleted(token, sessionId, progCallId);
    const snap = JSON.parse(progRes.content).snapshot;

    this.step = "invokeCommand";
    const cmdCallId = await this.invokeTool(token, sessionId, "command", {
      command: "printf 'wkwebview-receipt-ok\\n' > wkwebview-proof.txt && cat wkwebview-proof.txt",
    });
    const cmdRes = await this.waitToolCompleted(token, sessionId, cmdCallId);
    const programId = cmdRes?.command_program?.program_id || "";

    this.step = "updateProgressClose";
    const doneProgId = await this.invokeTool(token, sessionId, "update_progress", {
      content: "## Progress\n- [x] Verify WKWebView receipt identity and reload\n",
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
    return await this.inspectRendered(token, sessionId, cmdCallId, programId);
  },

  async runAfterReload(token, sessionId, expectedCallId, expectedProgramId) {
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
    return await this.inspectRendered(token, sessionId, expectedCallId, expectedProgramId);
  },
};
