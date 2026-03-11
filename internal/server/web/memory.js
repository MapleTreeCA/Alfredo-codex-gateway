const sessionSelectEl = document.getElementById("memory-session-select");
const sessionInputEl = document.getElementById("memory-session-input");
const queryInputEl = document.getElementById("memory-query-input");
const limitInputEl = document.getElementById("memory-limit-input");
const pageSizeSelectEl = document.getElementById("memory-page-size-select");
const searchBtnEl = document.getElementById("memory-search-btn");
const recentBtnEl = document.getElementById("memory-recent-btn");
const refreshBtnEl = document.getElementById("memory-refresh-btn");
const prevBtnEl = document.getElementById("memory-prev-btn");
const nextBtnEl = document.getElementById("memory-next-btn");
const pageStatusEl = document.getElementById("memory-page-status");
const statusEl = document.getElementById("memory-status");
const resultsEl = document.getElementById("memory-results");

let state = {
  mode: "search",
  page: 1,
  pageSize: 50,
  total: 0,
  lastSessionID: "",
  lastQuery: "",
  hasMore: false,
};

function setStatus(message, level = "info") {
  if (!statusEl) return;
  statusEl.textContent = `Memory: ${message}`;
  statusEl.classList.remove("ok", "err");
  if (level === "ok") statusEl.classList.add("ok");
  if (level === "err") statusEl.classList.add("err");
}

function currentSessionID() {
  const custom = String(sessionInputEl?.value || "").trim();
  if (custom) return custom;
  return String(sessionSelectEl?.value || "").trim();
}

function parseLimit() {
  const value = Number.parseInt(String(limitInputEl?.value || "50"), 10);
  if (!Number.isFinite(value) || value <= 0) return 50;
  return Math.min(value, 500);
}

function currentPageSize() {
  const value = Number.parseInt(String(pageSizeSelectEl?.value || "50"), 10);
  if (!Number.isFinite(value) || value <= 0) return 50;
  return Math.min(value, 500);
}

async function fetchJSON(url, options = {}) {
  const resp = await fetch(url, options);
  const body = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw new Error(body.error || `HTTP ${resp.status}`);
  }
  return body;
}

function formatTime(unixMs) {
  const value = Number(unixMs || 0);
  if (!Number.isFinite(value) || value <= 0) return "-";
  return new Date(value).toLocaleString();
}

function shortenText(value, maxChars = 220) {
  const text = String(value || "").trim();
  if (text.length <= maxChars) return text;
  return `${text.slice(0, maxChars)}...`;
}

function renderResults(items) {
  if (!resultsEl) return;
  resultsEl.innerHTML = "";
  if (!Array.isArray(items) || items.length === 0) {
    const empty = document.createElement("div");
    empty.className = "hint";
    empty.textContent = "No memory items found.";
    resultsEl.appendChild(empty);
    return;
  }

  for (const item of items) {
    const card = document.createElement("div");
    card.className = "memory-item";

    const meta = document.createElement("div");
    meta.className = "memory-meta";
    const role = String(item.role || "-");
    const sessionID = String(item.session_id || "-");
    const ts = formatTime(item.created_at);
    meta.textContent = `${ts} | session=${sessionID} | role=${role}`;

    const text = document.createElement("div");
    text.className = "memory-text clamp";
    text.textContent = shortenText(item.text, 400);

    const toggle = document.createElement("button");
    toggle.className = "ghost small";
    toggle.type = "button";
    toggle.textContent = "Expand";
    toggle.addEventListener("click", () => {
      const isClamped = text.classList.contains("clamp");
      if (isClamped) {
        text.classList.remove("clamp");
        toggle.textContent = "Collapse";
      } else {
        text.classList.add("clamp");
        toggle.textContent = "Expand";
      }
    });

    card.appendChild(meta);
    card.appendChild(text);
    card.appendChild(toggle);
    resultsEl.appendChild(card);
  }
}

function fillSessions(sessions) {
  if (!sessionSelectEl) return;
  sessionSelectEl.innerHTML = "";
  const items = Array.isArray(sessions) ? sessions : [];

  const anyOption = document.createElement("option");
  anyOption.value = "";
  anyOption.textContent = "(All sessions)";
  sessionSelectEl.appendChild(anyOption);

  for (const session of items) {
    const sid = String(session.session_id || "").trim();
    if (!sid) continue;
    const option = document.createElement("option");
    option.value = sid;
    option.textContent = `${sid} (${session.entry_count || 0})`;
    sessionSelectEl.appendChild(option);
  }
}

function setPager(total, page, pageSize, hasMore) {
  state.total = Number(total || 0);
  state.page = Number(page || 1);
  state.pageSize = Number(pageSize || currentPageSize());
  state.hasMore = Boolean(hasMore);

  const pageCount = Math.max(1, Math.ceil(state.total / state.pageSize));
  if (pageStatusEl) {
    pageStatusEl.textContent = `Page ${state.page}/${pageCount} | total ${state.total}`;
  }
  if (prevBtnEl) prevBtnEl.disabled = state.page <= 1;
  if (nextBtnEl) nextBtnEl.disabled = !state.hasMore;
}

async function loadSessions() {
  try {
    const data = await fetchJSON("/api/memory/sessions?limit=200");
    const sessions = Array.isArray(data.sessions) ? data.sessions : [];
    fillSessions(sessions);
    setStatus(`loaded ${sessions.length} sessions`, "ok");
  } catch (err) {
    fillSessions([]);
    setStatus(`failed to load sessions: ${err.message}`, "err");
  }
}

async function searchMemory(page = 1) {
  const sid = currentSessionID();
  const q = String(queryInputEl?.value || "").trim();
  const limit = parseLimit();
  const pageSize = currentPageSize();
  const params = new URLSearchParams();
  if (sid) params.set("session_id", sid);
  if (q) params.set("q", q);
  params.set("limit", String(limit));
  params.set("page", String(page));
  params.set("page_size", String(pageSize));

  try {
    const data = await fetchJSON(`/api/memory/search?${params.toString()}`);
    const items = Array.isArray(data.items) ? data.items : [];
    renderResults(items);
    setPager(data.total, data.page, data.page_size, data.has_more);
    setStatus(`search ok, ${items.length} items`, "ok");

    state.mode = "search";
    state.lastSessionID = sid;
    state.lastQuery = q;
  } catch (err) {
    renderResults([]);
    setPager(0, 1, currentPageSize(), false);
    setStatus(`search failed: ${err.message}`, "err");
  }
}

async function loadRecent(page = 1) {
  const sid = currentSessionID();
  if (!sid) {
    setStatus("select a session or set custom session id for recent", "err");
    return;
  }
  const limit = parseLimit();
  const pageSize = currentPageSize();
  const params = new URLSearchParams({
    session_id: sid,
    limit: String(limit),
    page: String(page),
    page_size: String(pageSize),
  });

  try {
    const data = await fetchJSON(`/api/memory/recent?${params.toString()}`);
    const items = Array.isArray(data.items) ? data.items : [];
    renderResults(items);
    setPager(data.total, data.page, data.page_size, data.has_more);
    setStatus(`recent ok, ${items.length} items`, "ok");

    state.mode = "recent";
    state.lastSessionID = sid;
    state.lastQuery = "";
  } catch (err) {
    renderResults([]);
    setPager(0, 1, currentPageSize(), false);
    setStatus(`recent failed: ${err.message}`, "err");
  }
}

function runCurrentMode(page) {
  if (state.mode === "recent") {
    loadRecent(page);
    return;
  }
  searchMemory(page);
}

searchBtnEl?.addEventListener("click", () => searchMemory(1));
recentBtnEl?.addEventListener("click", () => loadRecent(1));
refreshBtnEl?.addEventListener("click", loadSessions);
prevBtnEl?.addEventListener("click", () => {
  if (state.page <= 1) return;
  runCurrentMode(state.page - 1);
});
nextBtnEl?.addEventListener("click", () => {
  if (!state.hasMore) return;
  runCurrentMode(state.page + 1);
});
queryInputEl?.addEventListener("keydown", (event) => {
  if (event.key === "Enter") {
    event.preventDefault();
    searchMemory(1);
  }
});
pageSizeSelectEl?.addEventListener("change", () => {
  runCurrentMode(1);
});

setPager(0, 1, currentPageSize(), false);
loadSessions();
renderResults([]);
