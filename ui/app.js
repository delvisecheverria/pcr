/* ---------------------------------------
   Pulse Web UI — Full Functional Version
   --------------------------------------- */

const API_BASE = "http://localhost:5050/api";

/* ---------------------------------------
   DOM cache
--------------------------------------- */
const startBtn = document.getElementById("start");
const stopBtn = document.getElementById("stop");
const statusBtn = document.getElementById("status");
const indicator = document.getElementById("indicator");
const statusText = document.getElementById("status-text");
const logEl = document.getElementById("log");

const requestsList = document.getElementById("requests-list");
const requestDetail = document.getElementById("request-detail");

const headProtocol = document.getElementById("req-protocol");
const headServer   = document.getElementById("req-server");
const headPort     = document.getElementById("req-port");
const headMethod   = document.getElementById("req-method");
const headPath     = document.getElementById("req-path");

const tabButtons = document.querySelectorAll(".tab");
const tabContainers = document.querySelectorAll(".tab-content");
const elHeaders = document.getElementById("headers");
const elBody    = document.getElementById("body");
const elResp    = document.getElementById("response");
const elMeta    = document.getElementById("meta");

const recordingsList = document.getElementById("recordings-list");
const refreshBtn = document.getElementById("refresh-recordings");

const modal = document.getElementById("yaml-modal");
const modalTitle = document.getElementById("yaml-title");
const modalContent = document.getElementById("yaml-content");
const closeModal = document.getElementById("close-modal");
const downloadBtn = document.getElementById("download-yaml");

let recordedRequests = [];
let selectedIndex = -1;
let currentYAML = null;

/* ---------------------------------------
   Logging helpers
--------------------------------------- */
function logMessage(msg) {
  if (!logEl) return;
  const time = new Date().toLocaleTimeString();
  logEl.textContent += `[${time}] ${msg}\n`;
  logEl.scrollTop = logEl.scrollHeight;
}

function setStatus(running) {
  if (!indicator || !statusText) return;
  indicator.classList.toggle("running", running);
  indicator.classList.toggle("idle", !running);
  statusText.textContent = running ? "Running" : "Idle";
}

function highlightBlock(el, langHint = "json") {
  if (!el) return;
  const code = el.textContent;
  if (window.hljs && code) {
    try {
      el.innerHTML = window.hljs.highlight(code, { language: langHint }).value;
    } catch {
      el.textContent = code;
    }
  }
}

/* ---------------------------------------
   Normalizador de eventos
--------------------------------------- */
function normalizeEvent(ev) {
  const Method = ev.Method || ev.method || ev.HTTPMethod || ev.http_method || "GET";
  const URL = ev.URL || ev.url || ev.Request || ev.request_url || "";
  const Status = Number(ev.StatusCode || ev.status || ev.Status || ev.code || 0);
  const Time = ev.Time || ev.time || new Date().toISOString();

  // Acepta headers tanto como objeto como string
  let Headers = {};
  try {
    Headers = typeof ev.Headers === "string" ? JSON.parse(ev.Headers) :
              ev.Headers || ev.headers || {};
  } catch {
    Headers = ev.Headers || ev.headers || {};
  }

  const Body = ev.Body || ev.body || "";
  const Response = ev.Response || ev.response || "";

  // Extrae protocolo, host, path y puerto
  let Host = "", Path = "", Proto = "", Port = "";
  try {
    if (URL) {
      const u = new URL(URL);
      Host = u.hostname || "(unknown)";
      Path = u.pathname + (u.search || "");
      Proto = u.protocol ? u.protocol.replace(":", "") : "(unknown)";
      Port = u.port || (u.protocol === "https:" ? "443" : "80");
    }
  } catch {
    Path = URL;
  }

  return {
    time: Time,
    method: Method,
    url: URL,
    host: Host,
    proto: Proto,
    port: Port,
    path: Path,
    status: Status,
    headers: Headers,
    body: typeof Body === "string" ? Body : JSON.stringify(Body, null, 2),
    response: typeof Response === "string" ? Response : JSON.stringify(Response, null, 2),
  };
}

/* ---------------------------------------
   Recorder control
--------------------------------------- */
if (startBtn) {
  startBtn.onclick = async () => {
    startBtn.disabled = true;
    logMessage("▶️ Starting recorder...");
    try {
      const res = await fetch(`${API_BASE}/start-record`, { method: "POST" });
      const data = await res.json();
      logMessage(data.message);
      setStatus(true);
      stopBtn.disabled = false;
    } catch (err) {
      logMessage("❌ Failed to start recorder: " + err.message);
      startBtn.disabled = false;
    }
  };
}

if (stopBtn) {
  stopBtn.onclick = async () => {
    stopBtn.disabled = true;
    logMessage("🛑 Stopping recorder...");
    try {
      const res = await fetch(`${API_BASE}/stop-record`, { method: "POST" });
      const data = await res.json();
      logMessage(data.message);
    } catch (err) {
      logMessage("❌ Failed to stop recorder: " + err.message);
    }
    startBtn.disabled = false;
    setStatus(false);
    loadRecordings();
  };
}

if (statusBtn) {
  statusBtn.onclick = async () => {
    logMessage("🔍 Checking server status...");
    try {
      const res = await fetch(`${API_BASE}/status`);
      const data = await res.json();
      logMessage(`📡 ${data.message}`);
      setStatus(data.running);
    } catch (err) {
      logMessage("❌ Could not reach server: " + err.message);
    }
  };
}

/* ---------------------------------------
   SSE Logs & Requests
--------------------------------------- */
try {
  const evtSource = new EventSource(`${API_BASE}/logs`);
  evtSource.onmessage = (e) => handleEvent(e.data);
  evtSource.onerror = () => logMessage("⚠️ Lost connection to /api/logs");
} catch (err) {
  logMessage("❌ SSE connection failed: " + err.message);
}

function handleEvent(raw) {
  if (typeof raw === "string" && raw.startsWith("🔄 heartbeat")) return;
  logMessage(raw);

  try {
    const ev = JSON.parse(raw);
    if ((ev.Method || ev.method) && (ev.URL || ev.url)) {
      const item = normalizeEvent(ev);
      recordedRequests.push(item);
      renderRequests();
    }
  } catch {
    // plain log
  }
}

/* ---------------------------------------
   Render de lista de requests
--------------------------------------- */
function renderRequests() {
  if (!requestsList) return;
  requestsList.innerHTML = "";

  if (!recordedRequests.length) {
    requestsList.innerHTML = `<div class="empty">No requests recorded yet.</div>`;
    return;
  }

  recordedRequests.forEach((req, i) => {
    const row = document.createElement("div");
    const statusClass =
      req.status >= 200 && req.status < 300 ? "ok" :
      req.status >= 400 ? "err" :
      req.status === 0 ? "pending" : "warn";

    row.className = `req-row ${selectedIndex === i ? "selected" : ""}`;
    row.dataset.index = i;
    row.innerHTML = `
      <div class="req-main">
        <button class="chev">▶</button>
        <span class="req-method method-${req.method}">${req.method}</span>
        <span class="req-path">${escapeHTML(req.path || req.url)}</span>
        <span class="req-status ${statusClass}">${req.status ?? "-"}</span>
      </div>
      <div class="req-sub hidden">${renderHeaderPairs(req.headers)}</div>
    `;

    const chev = row.querySelector(".chev");
    const sub = row.querySelector(".req-sub");
    chev.addEventListener("click", (e) => {
      e.stopPropagation();
      const isHidden = sub.classList.contains("hidden");
      sub.classList.toggle("hidden", !isHidden);
      chev.textContent = isHidden ? "▼" : "▶";
    });

    row.addEventListener("click", () => {
      selectedIndex = i;
      document.querySelectorAll(".req-row").forEach(r => r.classList.remove("selected"));
      row.classList.add("selected");
      fillDetail(req);
    });

    requestsList.appendChild(row);
  });
}

function renderHeaderPairs(headers = {}) {
  const keys = Object.keys(headers);
  if (!keys.length) return `<div class="headers-empty">No headers</div>`;
  return `
    <table class="headers-table">
      <tbody>${keys.map(k => `
        <tr><th>${escapeHTML(k)}</th><td>${escapeHTML(String(headers[k]))}</td></tr>
      `).join("")}</tbody>
    </table>
  `;
}

/* ---------------------------------------
   Panel de detalle (split derecho)
--------------------------------------- */
function fillDetail(req) {
  if (!requestDetail) return;
  requestDetail.classList.remove("hidden");

  headProtocol.textContent = req.proto || "(unknown)";
  headServer.textContent = req.host || "(unknown)";
  headPort.textContent = req.port || inferPort(req.url);
  headMethod.textContent = req.method || "-";
  headPath.textContent = req.path || safePath(req.url);

  elHeaders.textContent = JSON.stringify(req.headers || {}, null, 2);
  highlightBlock(elHeaders, "json");

  elBody.textContent = tryPretty(req.body);
  highlightBlock(elBody, looksLikeJson(req.body) ? "json" : "plaintext");

  elResp.textContent = tryPretty(req.response);
  highlightBlock(elResp, looksLikeJson(req.response) ? "json" : "plaintext");

  elMeta.textContent = JSON.stringify({
    time: req.time,
    url: req.url,
    host: req.host,
    port: req.port,
    status: req.status
  }, null, 2);
  highlightBlock(elMeta, "json");
}

/* Helpers */
function safePath(u) {
  try { return new URL(u).pathname || "/"; } catch { return u || "/"; }
}
function inferPort(u) {
  try {
    const parsed = new URL(u);
    return parsed.port || (parsed.protocol === "https:" ? "443" : "80");
  } catch { return "-"; }
}
function tryPretty(s) {
  if (!s) return "(empty)";
  try { return JSON.stringify(JSON.parse(s), null, 2); }
  catch { return s; }
}
function looksLikeJson(s) {
  const t = String(s).trim();
  return (t.startsWith("{") && t.endsWith("}")) || (t.startsWith("[") && t.endsWith("]"));
}
function escapeHTML(s) {
  return String(s)
    .replaceAll("&","&amp;").replaceAll("<","&lt;").replaceAll(">","&gt;");
}

/* ---------------------------------------
   Tabs switching
--------------------------------------- */
function activateTab(id) {
  tabButtons.forEach(btn => btn.classList.toggle("active", btn.dataset.tab === id));
  tabContainers.forEach(c => c.classList.toggle("hidden", c.id !== id));
}
tabButtons.forEach(btn => btn.addEventListener("click", () => activateTab(btn.dataset.tab)));

/* ---------------------------------------
   Recordings Panel
--------------------------------------- */
async function loadRecordings() {
  if (!recordingsList) return;
  recordingsList.innerHTML = "<li>Loading...</li>";
  try {
    const res = await fetch(`${API_BASE}/recordings`);
    const files = await res.json();
    recordingsList.innerHTML = "";
    files.forEach((f) => {
      const li = document.createElement("li");
      li.innerHTML = `
        <a href="#" onclick="previewYAML('${f}')">${f}</a>
        <button class="download-btn" onclick="downloadRecording('${f}')">⬇️</button>
      `;
      recordingsList.appendChild(li);
    });
  } catch (err) {
    recordingsList.innerHTML = `<li>❌ ${err.message}</li>`;
  }
}
if (refreshBtn) refreshBtn.addEventListener("click", loadRecordings);
window.addEventListener("DOMContentLoaded", loadRecordings);

/* ---------------------------------------
   YAML Modal
--------------------------------------- */
async function previewYAML(filename) {
  modal.classList.remove("hidden");
  modalTitle.textContent = filename;
  modalContent.textContent = "Loading...";
  currentYAML = filename;
  try {
    const res = await fetch(`http://localhost:5050/examples/${filename}`);
    const text = await res.text();
    modalContent.innerHTML = `<code class="language-yaml">${hljs.highlight(text, { language: "yaml" }).value}</code>`;
  } catch (err) {
    modalContent.textContent = "❌ " + err.message;
  }
}
window.previewYAML = previewYAML;

function downloadRecording(filename) {
  const url = `http://localhost:5050/examples/${filename}`;
  const a = document.createElement("a");
  a.href = url; a.download = filename;
  document.body.appendChild(a);
  a.click(); a.remove();
}
window.downloadRecording = downloadRecording;

if (closeModal) closeModal.addEventListener("click", () => modal.classList.add("hidden"));
if (modal) modal.addEventListener("click", (e) => { if (e.target === modal) modal.classList.add("hidden"); });

/* ---------------------------------------
   Theme toggle
--------------------------------------- */
const themeToggle = document.getElementById("theme-toggle");
if (themeToggle) {
  const saved = localStorage.getItem("pulse-theme");
  if (saved === "dark") { document.body.classList.add("dark"); themeToggle.checked = true; }
  themeToggle.addEventListener("change", (e) => {
    document.body.classList.toggle("dark", e.target.checked);
    localStorage.setItem("pulse-theme", e.target.checked ? "dark" : "light");
  });
}

/* ---------------------------------------
   Auto-init
--------------------------------------- */
(async function init() {
  try {
    const res = await fetch(`${API_BASE}/status`);
    const data = await res.json();
    logMessage(`💡 Pulse UI ready — ${data.message}`);
    setStatus(data.running);
  } catch {
    logMessage("⚠️ Could not connect to Pulse backend at " + API_BASE);
  }
})();
