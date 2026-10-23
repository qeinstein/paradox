"use strict";

const state = {
  apiKey: sessionStorage.getItem("paradox_api_key") || "",
  runs: [],
  evals: [],
  selectedRunID: null,
  detail: null,
  events: [],
  eventSeqs: new Set(),
  eventSource: null,
  selectedNodeID: null,
  activeTab: "timeline",
  refreshTimer: null,
};

const $ = (id) => document.getElementById(id);

function esc(value) {
  return String(value == null ? "" : value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

function truncate(value, limit) {
  const text = String(value == null ? "" : value).replace(/\s+/g, " ").trim();
  if (text.length <= limit) return text;
  return text.slice(0, limit) + "…";
}

function fmtPct(value) {
  if (value == null || Number.isNaN(value)) return "n/a";
  return (value * 100).toFixed(1) + "%";
}

function fmtCost(usage) {
  if (!usage || !usage.cost_available) return "n/a";
  return "$" + Number(usage.cost_usd || 0).toFixed(6);
}

function fmtDuration(ms) {
  if (!ms) return "n/a";
  if (ms < 1000) return Math.round(ms) + " ms";
  if (ms < 60000) return (ms / 1000).toFixed(2) + " s";
  return Math.floor(ms / 60000) + "m " + Math.round((ms % 60000) / 1000) + "s";
}

function fmtTime(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString();
}

function relativeTime(value) {
  if (!value) return "";
  const date = new Date(value);
  const seconds = Math.max(0, (Date.now() - date.getTime()) / 1000);
  if (seconds < 60) return Math.floor(seconds) + "s ago";
  if (seconds < 3600) return Math.floor(seconds / 60) + "m ago";
  if (seconds < 86400) return Math.floor(seconds / 3600) + "h ago";
  return Math.floor(seconds / 86400) + "d ago";
}

function authHeaders(extra) {
  const headers = Object.assign({}, extra || {});
  if (state.apiKey) headers["X-API-Key"] = state.apiKey;
  return headers;
}

async function api(path, options, retry) {
  const request = Object.assign({}, options || {});
  request.headers = authHeaders(request.headers);
  const response = await fetch(path, request);
  if (response.status === 401 && retry !== false) {
    const key = window.prompt("This PARADOX server requires an API key. Enter PARADOX_API_KEY:");
    if (key) {
      state.apiKey = key.trim();
      sessionStorage.setItem("paradox_api_key", state.apiKey);
      return api(path, options, false);
    }
  }
  if (!response.ok) {
    let message = response.statusText;
    try {
      const body = await response.json();
      if (body && body.error) message = body.error;
    } catch (_) {
      // Keep the status text.
    }
    throw new Error(message || ("HTTP " + response.status));
  }
  return response.json();
}

function setConnection(ok) {
  const element = $("connection");
  element.classList.toggle("ok", ok);
  element.classList.toggle("bad", !ok);
  element.textContent = ok ? "connected" : "offline";
}

function toast(message, isError) {
  const element = $("toast");
  element.textContent = message;
  element.classList.remove("hidden");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => element.classList.add("hidden"), isError === false ? 3000 : 6000);
}

function badge(text, className) {
  return '<span class="badge ' + esc(className || "") + '">' + esc(text) + "</span>";
}

function statusClass(status) {
  if (status === "completed") return "success";
  if (status === "failed" || status === "cancelled") return "failed";
  if (status === "running" || status === "starting") return "running";
  return "interrupted";
}

async function loadHealth() {
  try {
    const health = await api("/api/health");
    $("version").textContent = health.version ? "v" + health.version : "";
    setConnection(true);
  } catch (error) {
    setConnection(false);
    throw error;
  }
}

async function loadRuns() {
  const payload = await api("/api/runs?limit=100");
  state.runs = payload.runs || [];
  $("run-count").textContent = state.runs.length + (state.runs.length === 1 ? " run" : " runs");
  renderRuns();
  if (!state.selectedRunID && state.runs.length > 0) {
    const fromHash = location.hash.startsWith("#run/") ? location.hash.slice(5) : "";
    const target = state.runs.find((run) => run.manifest.run_id === fromHash) || state.runs[0];
    await selectRun(target.manifest.run_id);
  }
}

function renderRuns() {
  const container = $("runs");
  if (state.runs.length === 0) {
    container.innerHTML = '<div class="run-item"><div class="run-item-task">No runs yet.</div><div class="run-item-meta">Start a run to populate the trace.</div></div>';
    return;
  }
  container.innerHTML = state.runs.map((item) => {
    const manifest = item.manifest;
    const selected = manifest.run_id === state.selectedRunID ? " selected" : "";
    const score = item.score == null ? "" : " · score " + Number(item.score).toFixed(2);
    return (
      '<button class="run-item' + selected + '" data-run="' + esc(manifest.run_id) + '" type="button">' +
        '<div class="run-item-top">' +
          '<span class="run-item-id">' + esc(manifest.run_id) + "</span>" +
          badge(manifest.mode, manifest.mode) +
        "</div>" +
        '<div class="run-item-task">' + esc(truncate(manifest.task_prompt || manifest.task_id, 80)) + "</div>" +
        '<div class="run-item-meta">' + esc(manifest.provider + " / " + manifest.model + " · " + relativeTime(manifest.created_at) + score) + "</div>" +
      "</button>"
    );
  }).join("");
  container.querySelectorAll("[data-run]").forEach((element) => {
    element.addEventListener("click", () => selectRun(element.dataset.run));
  });
}

async function selectRun(runID) {
  if (!runID) return;
  closeStream();
  state.selectedRunID = runID;
  state.selectedNodeID = null;
  state.events = [];
  state.eventSeqs = new Set();
  history.replaceState(null, "", "#run/" + encodeURIComponent(runID));
  try {
    const detail = await api("/api/runs/" + encodeURIComponent(runID));
    state.detail = detail;
    renderRuns();
    renderRun(detail);
    const events = await api("/api/runs/" + encodeURIComponent(runID) + "/events");
    state.events = Array.isArray(events) ? events : [];
    for (const event of state.events) {
      if (event && event.seq) state.eventSeqs.add(event.seq);
    }
    renderTimeline();
    if (detail.status === "running" || detail.status === "starting") {
      openStream(runID);
    }
  } catch (error) {
    toast("Could not load run: " + error.message);
  }
}

function renderRun(detail) {
  $("empty").classList.add("hidden");
  $("run-view").classList.remove("hidden");
  const manifest = detail.manifest || {};
  const result = detail.result;
  const stats = (detail.evidence && detail.evidence.stats) || {};
  $("run-id").textContent = manifest.run_id || state.selectedRunID;
  $("run-task").textContent = truncate(manifest.task_prompt || manifest.task_id, 180);
  $("run-meta").textContent = [
    manifest.task_id,
    manifest.domain,
    manifest.mode,
    manifest.provider + " / " + manifest.model,
    manifest.git_commit ? "commit " + manifest.git_commit.slice(0, 12) : "",
    fmtTime(manifest.created_at),
  ].filter(Boolean).join(" · ");
  const status = $("run-status");
  status.className = "badge " + statusClass(detail.status);
  status.textContent = detail.status + (result && result.success ? " · passed" : "");
  $("cancel-run").classList.toggle("hidden", !(detail.status === "running" || detail.status === "starting"));

  renderStats(detail, stats);
  renderGraph(detail.evidence);
  renderResult(detail);
  $("raw").textContent = JSON.stringify(detail, null, 2);
  setLive(detail.status === "running" || detail.status === "starting");
}

function stat(label, value, sub) {
  return '<div class="stat"><div class="stat-label">' + esc(label) + '</div><div class="stat-value">' + esc(value) + '</div><div class="stat-sub">' + esc(sub || "") + "</div></div>";
}

function renderStats(detail, stats) {
  const result = detail.result;
  const usage = (result && result.usage) || {};
  const unsupported = stats.active_claims ? fmtPct(stats.unsupported_claim_rate) : "n/a";
  const verification = stats.verifications || 0;
  const cards = [
    stat("Status", detail.status, result ? (result.success ? "evaluation passed" : "evaluation not passed") : "executing"),
    stat("Score", result ? Number(result.score || 0).toFixed(3) : "—", result && result.evaluation ? "domain evaluation" : ""),
    stat("Observations", stats.observations || 0, "direct tool evidence"),
    stat("Active claims", stats.active_claims || 0, (stats.claims || 0) + " total claims"),
    stat("Unsupported", unsupported, (stats.unsupported_claims || 0) + " active unsupported"),
    stat("Contradictions", stats.contradictions || 0, (stats.retractions || 0) + " retractions"),
    stat("Verifications", verification, (stats.assumptions || 0) + " assumptions"),
    stat("Tokens", usage.total_tokens || 0, (usage.prompt_tokens || 0) + " prompt / " + (usage.completion_tokens || 0) + " completion"),
    stat("Cost", fmtCost(usage), usage.cost_available ? "provider-reported" : "not reported"),
    stat("Duration", fmtDuration(result ? result.duration_ms : 0), result ? result.steps + " steps" : ""),
    stat("Inference", usage.inference_calls || 0, "model calls"),
    stat("Tools", usage.tool_calls || 0, "executed calls"),
  ];
  $("stats").innerHTML = cards.join("");
}

function eventSummary(event) {
  const data = event.data || {};
  switch (event.type) {
    case "run_started":
      return "task=" + (data.task_id || "") + " mode=" + (data.mode || "") + " provider=" + (data.provider || "") + "/" + (data.model || "");
    case "provider_request":
      return "model=" + (data.model || "") + " messages=" + (data.messages || 0) + " tools=" + (data.tools || 0) + " max_tokens=" + (data.max_tokens || 0);
    case "provider_response": {
      const content = data.content ? truncate(data.content, 180) : "tool_calls=" + ((data.tool_calls || []).length);
      const usage = data.usage || {};
      return content + " (tokens=" + (usage.total_tokens || 0) + " finish=" + (data.finish_reason || "") + ")";
    }
    case "provider_error":
      return "attempt=" + (data.attempt || 0) + " " + (data.error || "");
    case "assistant_message": {
      if (data.content) return truncate(data.content, 200);
      const calls = data.tool_calls || [];
      return "tool_calls=" + calls.map((call) => call.name).join(",");
    }
    case "tool_call":
      return (data.tool || "") + " origin=" + (data.origin || "") + " args=" + truncate(data.arguments || "", 180);
    case "tool_result":
      return (data.tool || "") + " ok=" + data.ok + " evidence=" + ((data.evidence_ids || []).join(",") || "none");
    case "observation": {
      const node = data.node || {};
      return (node.id || "") + " " + truncate(node.text || "", 220);
    }
    case "claim_submitted": {
      const claim = data.claim || {};
      return (claim.id || "") + " " + truncate(claim.text || "", 220) + " guard=" + (data.guard_id || "");
    }
    case "assumption_recorded": {
      const node = data.node || {};
      return (node.id || "") + " " + truncate(node.text || "", 220);
    }
    case "verification_planned": {
      const plan = data.plan || {};
      return (plan.id || "") + " claim=" + (data.claim_id || "") + " " + truncate(plan.rationale || "", 240);
    }
    case "verification_passed":
      return "claim=" + (data.claim_id || "") + " assumption=" + (data.assumption_id || "") + " " + truncate(data.reason || "", 200);
    case "contradiction_detected": {
      const contradiction = data.contradiction || {};
      return "claim=" + (contradiction.claim_id || "") + " evidence=" + (contradiction.evidence_id || "") + " " + truncate(contradiction.reason || "", 240);
    }
    case "claim_invalidated":
      return "claim=" + (data.claim_id || "") + " " + truncate(data.reason || "", 240);
    case "replan_required":
      return truncate(data.reason || "", 240);
    case "consistency_check":
      return "issues=" + (data.issue_count || 0);
    case "budget_exhausted":
      return "kind=" + (data.kind || "") + " used=" + data.used + " limit=" + data.limit;
    case "run_finished":
      return "success=" + data.success + " score=" + data.score + " error=" + truncate(data.error || "", 160);
    case "agent_finished":
      return truncate(data.conclusion || "", 240);
    case "error":
    case "persistence_error":
      return truncate(data.error || "", 300);
    case "cost_unavailable":
      return truncate(data.message || "", 200);
    default:
      return truncate(JSON.stringify(data), 240);
  }
}

function renderTimeline() {
  const container = $("timeline");
  if (state.events.length === 0) {
    container.innerHTML = '<div class="event"><div class="event-summary muted">No events recorded yet.</div></div>';
    return;
  }
  container.innerHTML = state.events.map((event) => {
    const time = event.time ? new Date(event.time).toLocaleTimeString([], { hour12: false }) : "";
    return (
      '<div class="event">' +
        '<div class="event-seq">#' + String(event.seq || 0).padStart(4, "0") + "</div>" +
        '<div class="event-time">' + esc(time) + "</div>" +
        '<div class="event-type">' + esc(event.type) + "</div>" +
        '<div class="event-summary">' + esc(eventSummary(event)) + "</div>" +
      "</div>"
    );
  }).join("");
  container.scrollTop = container.scrollHeight;
}

function setLive(live) {
  $("live-dot").classList.toggle("live", live);
  $("live-label").textContent = live ? "Live event stream" : "Persisted events";
}

function openStream(runID) {
  closeStream();
  const query = state.apiKey ? "?api_key=" + encodeURIComponent(state.apiKey) : "";
  const source = new EventSource("/api/runs/" + encodeURIComponent(runID) + "/events/stream" + query);
  state.eventSource = source;
  let refreshQueued = false;
  const queueRefresh = () => {
    if (refreshQueued) return;
    refreshQueued = true;
    setTimeout(async () => {
      refreshQueued = false;
      if (state.selectedRunID !== runID) return;
      try {
        const detail = await api("/api/runs/" + encodeURIComponent(runID));
        state.detail = detail;
        renderRun(detail);
      } catch (_) {
        // The stream will report the terminal state.
      }
    }, 250);
  };
  source.onmessage = (message) => {
    let event;
    try {
      event = JSON.parse(message.data);
    } catch (_) {
      return;
    }
    if (event.type === "server_run_finished") {
      closeStream();
      setLive(false);
      loadRuns().catch((error) => toast(error.message));
      loadEvals().catch(() => {});
      api("/api/runs/" + encodeURIComponent(runID))
        .then((detail) => {
          state.detail = detail;
          renderRun(detail);
        })
        .catch((error) => toast(error.message));
      return;
    }
    if (event.seq && !state.eventSeqs.has(event.seq)) {
      state.eventSeqs.add(event.seq);
      state.events.push(event);
      state.events.sort((a, b) => (a.seq || 0) - (b.seq || 0));
      renderTimeline();
      if (["claim_submitted", "claim_invalidated", "contradiction_detected", "verification_passed", "run_finished", "observation"].includes(event.type)) {
        queueRefresh();
      }
    }
  };
  source.onerror = () => {
    setLive(false);
    // EventSource reconnects automatically. The persisted trace remains the source of truth.
  };
}

function closeStream() {
  if (state.eventSource) {
    state.eventSource.close();
    state.eventSource = null;
  }
}

function renderGraph(snapshot) {
  const container = $("graph");
  if (!snapshot || !snapshot.nodes || snapshot.nodes.length === 0) {
    container.innerHTML = '<div class="node-detail">No evidence has been recorded yet.</div>';
    $("node-detail").textContent = "";
    return;
  }
  const nodes = snapshot.nodes.filter((node) => !node.internal);
  const groups = { observation: [], assumption: [], claim: [], contradiction: [] };
  for (const node of nodes) {
    if (groups[node.kind]) groups[node.kind].push(node);
  }
  const columns = [
    { x: 20, width: 250, kinds: ["observation", "assumption"], label: "Evidence & assumptions" },
    { x: 330, width: 250, kinds: ["claim"], label: "Claims" },
    { x: 640, width: 250, kinds: ["contradiction"], label: "Contradictions" },
  ];
  const nodeWidth = 250;
  const nodeHeight = 50;
  const rowGap = 66;
  let maxRows = 1;
  for (const column of columns) {
    const count = column.kinds.reduce((sum, kind) => sum + groups[kind].length, 0);
    maxRows = Math.max(maxRows, count);
  }
  const height = Math.max(280, 64 + maxRows * rowGap + 20);
  const positions = {};
  for (const column of columns) {
    let row = 0;
    for (const kind of column.kinds) {
      for (const node of groups[kind]) {
        positions[node.id] = {
          x: column.x, y: 52 + row * rowGap, w: nodeWidth, h: nodeHeight, node: node,
        };
        row++;
      }
    }
  }
  const edges = (snapshot.edges || []).filter((edge) => positions[edge.from] && positions[edge.to] && edge.from !== edge.to);
  const edgeMarkup = edges.map((edge) => {
    const from = positions[edge.from];
    const to = positions[edge.to];
    const x1 = from.x + from.w;
    const y1 = from.y + from.h / 2;
    const x2 = to.x;
    const y2 = to.y + to.h / 2;
    const dx = Math.max(30, Math.abs(x2 - x1) / 2);
    const path = "M" + x1 + "," + y1 + " C" + (x1 + dx) + "," + y1 + " " + (x2 - dx) + "," + y2 + " " + x2 + "," + y2;
    const invalid = (!from.node.active || !to.node.active) ? " invalid" : "";
    return '<path class="edge' + invalid + '" d="' + path + '" marker-end="url(#arrow)"></path>';
  }).join("");
  const columnMarkup = columns.map((column) =>
    '<text class="column-label" x="' + column.x + '" y="26">' + esc(column.label) + "</text>"
  ).join("");
  const nodeMarkup = Object.values(positions).map((position) => {
    const node = position.node;
    const active = node.active ? "" : " invalid";
    const label = (node.claim_key || node.kind) + (node.claim_value ? " = " + node.claim_value : "");
    const text = truncate(node.text || "", 38);
    const status = "status " + Number(node.status || 0).toFixed(2) + " · " + (node.verification || "unverified");
    return (
      '<g class="node ' + esc(node.kind) + active + '" data-node="' + esc(node.id) + '">' +
        '<title>' + esc(node.text || "") + "</title>" +
        '<rect x="' + position.x + '" y="' + position.y + '" width="' + position.w + '" height="' + position.h + '" rx="7"></rect>' +
        '<text x="' + (position.x + 10) + '" y="' + (position.y + 15) + '" class="node-kind">' + esc(label) + "</text>" +
        '<text x="' + (position.x + 10) + '" y="' + (position.y + 30) + '">' + esc(text) + "</text>" +
        '<text x="' + (position.x + 10) + '" y="' + (position.y + 43) + '" class="node-kind">' + esc(status) + "</text>" +
      "</g>"
    );
  }).join("");
  container.innerHTML =
    '<svg viewBox="0 0 910 ' + height + '" role="img" aria-label="Evidence dependency graph">' +
      '<defs><marker id="arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">' +
        '<path d="M0,0 L8,4 L0,8 z" fill="#3a4b63"></path>' +
      "</marker></defs>" +
      columnMarkup + edgeMarkup + nodeMarkup +
    "</svg>";
  container.querySelectorAll("[data-node]").forEach((element) => {
    element.addEventListener("click", () => showNode(element.dataset.node));
  });
  if (state.selectedNodeID && positions[state.selectedNodeID]) {
    showNode(state.selectedNodeID);
  } else {
    state.selectedNodeID = null;
    $("node-detail").textContent = "Select a node to inspect its evidence, assumptions, and dependencies.";
  }
}

function showNode(nodeID) {
  const snapshot = state.detail && state.detail.evidence;
  if (!snapshot) return;
  const node = (snapshot.nodes || []).find((item) => item.id === nodeID);
  if (!node) return;
  state.selectedNodeID = nodeID;
  const edges = (snapshot.edges || []).filter((edge) => edge.from === nodeID || edge.to === nodeID);
  const lines = [];
  lines.push("<strong>" + esc(node.id) + "</strong> · " + esc(node.kind) + " · " + esc(node.active ? "ACTIVE" : "INVALID"));
  lines.push("status " + Number(node.status || 0).toFixed(2) + " · verification " + esc(node.verification || "unverified") + " · source " + esc(node.source || "unknown"));
  lines.push("");
  lines.push(esc(node.text || ""));
  if (node.claim_key || node.claim_value) lines.push("claim: " + esc(node.claim_key || "") + " = " + esc(node.claim_value || ""));
  if (node.evidence_ids && node.evidence_ids.length) lines.push("evidence: " + esc(node.evidence_ids.join(", ")));
  if (node.assumption_ids && node.assumption_ids.length) lines.push("assumptions: " + esc(node.assumption_ids.join(", ")));
  if (node.artifacts && node.artifacts.length) lines.push("artifacts: " + esc(node.artifacts.map((artifact) => artifact.name).join(", ")));
  if (edges.length) {
    lines.push("");
    lines.push("relations:");
    for (const edge of edges) {
      lines.push("  " + esc(edge.from) + " → " + esc(edge.to) + " (set " + edge.justification_set + (edge.negated ? ", negated" : "") + ")");
    }
  }
  $("node-detail").innerHTML = lines.join("\n");
}

function renderResult(detail) {
  const container = $("result");
  const result = detail.result;
  if (!result) {
    container.innerHTML = '<div class="result-block"><h3>Result</h3><p class="muted">The run is still executing. This view updates from the live event stream.</p></div>';
    return;
  }
  const evaluation = result.evaluation || {};
  const usage = result.usage || {};
  const details = evaluation.details || {};
  const detailRows = Object.keys(details).sort().map((key) =>
    "<dt>" + esc(key) + "</dt><dd>" + esc(String(details[key])) + "</dd>"
  ).join("");
  const artifacts = [];
  const seen = new Set();
  for (const node of ((detail.evidence && detail.evidence.nodes) || [])) {
    for (const artifact of (node.artifacts || [])) {
      if (!artifact.name || seen.has(artifact.name)) continue;
      seen.add(artifact.name);
      artifacts.push('<button class="artifact-button" data-artifact="' + esc(artifact.name) + '" type="button">↓ ' + esc(artifact.name) + "</button>");
    }
  }
  container.innerHTML =
    '<div class="result-grid">' +
      '<div class="result-block"><h3>Conclusion</h3><p>' + esc(result.final_text || "No conclusion recorded.") + "</p>" +
        (result.error ? '<p class="error-text">' + esc(result.error) + "</p>" : "") +
      "</div>" +
      '<div class="result-block"><h3>Evaluation</h3><dl class="kv">' +
        "<dt>success</dt><dd>" + esc(String(evaluation.success)) + "</dd>" +
        "<dt>score</dt><dd>" + Number(evaluation.score || 0).toFixed(3) + "</dd>" +
        detailRows +
      "</dl></div>" +
      '<div class="result-block"><h3>Usage</h3><dl class="kv">' +
        "<dt>prompt tokens</dt><dd>" + (usage.prompt_tokens || 0) + "</dd>" +
        "<dt>completion tokens</dt><dd>" + (usage.completion_tokens || 0) + "</dd>" +
        "<dt>reasoning tokens</dt><dd>" + (usage.reasoning_tokens || 0) + "</dd>" +
        "<dt>total tokens</dt><dd>" + (usage.total_tokens || 0) + "</dd>" +
        "<dt>cost</dt><dd>" + esc(fmtCost(usage)) + "</dd>" +
        "<dt>inference calls</dt><dd>" + (usage.inference_calls || 0) + "</dd>" +
        "<dt>tool calls</dt><dd>" + (usage.tool_calls || 0) + "</dd>" +
        "<dt>duration</dt><dd>" + esc(fmtDuration(result.duration_ms)) + "</dd>" +
      "</dl></div>" +
      '<div class="result-block"><h3>Artifacts</h3>' +
        (artifacts.length ? '<div class="artifact-list">' + artifacts.join("") + "</div>" : '<p class="muted">No artifacts.</p>') +
      "</div>" +
    "</div>";
  container.querySelectorAll("[data-artifact]").forEach((element) => {
    element.addEventListener("click", () => downloadArtifact(element.dataset.artifact));
  });
}

async function downloadArtifact(name) {
  if (!state.selectedRunID) return;
  try {
    const response = await fetch("/api/runs/" + encodeURIComponent(state.selectedRunID) + "/artifacts/" + encodeURIComponent(name), {
      headers: authHeaders(),
    });
    if (!response.ok) throw new Error("HTTP " + response.status);
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = name;
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(url), 2000);
  } catch (error) {
    toast("Could not download artifact: " + error.message);
  }
}

async function loadEvals() {
  const payload = await api("/api/evals");
  state.evals = payload.evals || [];
  renderEvals();
}

function renderEvals() {
  const container = $("evals");
  if (state.evals.length === 0) {
    container.innerHTML = '<div class="eval-item"><div class="eval-item-meta">No reports. Run <code>paradox eval</code>.</div></div>';
    return;
  }
  container.innerHTML = state.evals.map((item) => {
    const modes = (item.modes || []).join(" · ");
    return (
      '<button class="eval-item" data-eval="' + esc(item.eval_id) + '" type="button">' +
        '<div class="eval-item-id">' + esc(item.eval_id) + "</div>" +
        '<div class="eval-item-meta">' + esc(item.provider + " / " + item.model + " · " + item.trials + " trials") + "</div>" +
        '<div class="eval-item-meta">' + esc(modes + " · success " + fmtPct(item.overall && item.overall.success_rate)) + "</div>" +
      "</button>"
    );
  }).join("");
  container.querySelectorAll("[data-eval]").forEach((element) => {
    element.addEventListener("click", () => showEval(element.dataset.eval));
  });
}

async function showEval(evalID) {
  try {
    const report = await api("/api/evals/" + encodeURIComponent(evalID));
    $("eval-modal-title").textContent = "Evaluation · " + report.eval_id;
    const modes = report.modes || [];
    const rows = modes.map((mode) => {
      const aggregate = (report.by_mode || {})[mode] || {};
      return "<tr><td>" + esc(mode) + "</td><td class='num'>" + (aggregate.runs || 0) + "</td><td class='num'>" +
        fmtPct(aggregate.success_rate) + "</td><td class='num'>" + Number(aggregate.mean_score || 0).toFixed(3) +
        "</td><td class='num'>" + fmtPct(aggregate.unsupported_claim_rate) + "</td><td class='num'>" +
        fmtPct(aggregate.incorrect_retraction_rate) + "</td><td class='num'>" + fmtPct(aggregate.verification_accuracy) +
        "</td><td class='num'>" + (aggregate.total_tokens || 0) + "</td></tr>";
    }).join("");
    const runRows = (report.runs || []).map((run) =>
      "<tr><td>" + esc(run.run_id) + "</td><td>" + esc(run.mode) + "</td><td class='num'>" + run.trial +
      "</td><td>" + (run.success ? "yes" : "no") + "</td><td class='num'>" + Number(run.score || 0).toFixed(3) +
      "</td><td>" + esc(truncate(run.error || "", 120)) + "</td></tr>"
    ).join("");
    $("eval-modal-body").innerHTML =
      '<dl class="kv">' +
        "<dt>suite</dt><dd>" + esc(report.suite) + "</dd>" +
        "<dt>provider</dt><dd>" + esc(report.provider + " / " + report.model) + "</dd>" +
        "<dt>trials</dt><dd>" + (report.trials || 0) + " per mode</dd>" +
        "<dt>created</dt><dd>" + esc(fmtTime(report.created_at)) + "</dd>" +
      "</dl>" +
      '<h3 class="eyebrow" style="margin-top:16px">Mode comparison</h3>' +
      '<table class="data"><thead><tr><th>Mode</th><th>Runs</th><th>Success</th><th>Score</th><th>Unsupported</th><th>Bad retractions</th><th>Verification</th><th>Tokens</th></tr></thead><tbody>' + rows + "</tbody></table>" +
      '<h3 class="eyebrow" style="margin-top:16px">Runs</h3>' +
      '<table class="data"><thead><tr><th>Run</th><th>Mode</th><th>Trial</th><th>Success</th><th>Score</th><th>Error</th></tr></thead><tbody>' + runRows + "</tbody></table>" +
      '<h3 class="eyebrow" style="margin-top:16px">Notes</h3><ul>' + (report.notes || []).map((note) => "<li>" + esc(note) + "</li>").join("") + "</ul>";
    $("eval-modal").classList.remove("hidden");
  } catch (error) {
    toast("Could not load evaluation: " + error.message);
  }
}

function bindEvents() {
  $("refresh").addEventListener("click", () => {
    loadRuns().catch((error) => toast(error.message));
    loadEvals().catch((error) => toast(error.message));
  });
  $("refresh-evals").addEventListener("click", () => loadEvals().catch((error) => toast(error.message)));
  $("eval-modal-close").addEventListener("click", () => $("eval-modal").classList.add("hidden"));
  $("eval-modal").addEventListener("click", (event) => {
    if (event.target === $("eval-modal")) $("eval-modal").classList.add("hidden");
  });
  $("cancel-run").addEventListener("click", async () => {
    if (!state.selectedRunID) return;
    try {
      await api("/api/runs/" + encodeURIComponent(state.selectedRunID) + "/cancel", { method: "POST" });
      toast("Cancellation requested", false);
    } catch (error) {
      toast("Could not cancel: " + error.message);
    }
  });
  $("provider").addEventListener("change", (event) => {
    const provider = event.target.value;
    const model = $("model");
    if (provider === "mock") model.value = "mock-science-v1";
    else if (provider === "meta") model.value = "muse-spark-1.3";
    else if (provider === "openrouter") model.value = "";
    else model.value = "";
  });
  $("run-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const provider = $("provider").value;
    const model = $("model").value.trim();
    if (provider === "openrouter" && !model) {
      toast("OpenRouter requires an explicit model id");
      return;
    }
    const body = {
      task_id: $("task").value,
      mode: $("mode").value,
      provider: provider,
      model: model,
      budget: {
        max_steps: Number($("max-steps").value || 20),
        max_tool_calls: 40,
        max_tokens: Number($("max-tokens").value || 200000),
        max_cost_usd: 5,
        timeout_ms: 600000,
        max_retries: 2,
        tool_timeout_ms: 120000,
      },
    };
    try {
      const response = await api("/api/runs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      toast("Run started: " + response.run_id, false);
      await loadRuns();
      await selectRun(response.run_id);
    } catch (error) {
      toast("Could not start run: " + error.message);
    }
  });
  document.querySelectorAll(".tab").forEach((tab) => {
    tab.addEventListener("click", () => {
      state.activeTab = tab.dataset.tab;
      document.querySelectorAll(".tab").forEach((item) => item.classList.toggle("active", item === tab));
      document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.remove("active"));
      const panel = $("tab-" + state.activeTab);
      if (panel) panel.classList.add("active");
    });
  });
  window.addEventListener("hashchange", () => {
    if (location.hash.startsWith("#run/")) {
      const runID = decodeURIComponent(location.hash.slice(5));
      if (runID !== state.selectedRunID) selectRun(runID);
    }
  });
}

async function init() {
  bindEvents();
  try {
    await loadHealth();
    await loadRuns();
    await loadEvals();
  } catch (error) {
    setConnection(false);
    toast("Could not connect to the PARADOX API: " + error.message);
  }
  setInterval(async () => {
    try {
      await loadRuns();
      if (state.selectedRunID) {
        const detail = await api("/api/runs/" + encodeURIComponent(state.selectedRunID));
        if (detail.status !== (state.detail && state.detail.status) || detail.result) {
          state.detail = detail;
          renderRun(detail);
        }
      }
    } catch (_) {
      setConnection(false);
    }
  }, 5000);
}

document.addEventListener("DOMContentLoaded", init);
