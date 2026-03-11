const oauthStatusEl = document.getElementById("oauth-status");
const oauthLoginBtn = document.getElementById("oauth-login");
const oauthRefreshBtn = document.getElementById("oauth-refresh");
const chatLogEl = document.getElementById("chat-log");
const chatFormEl = document.getElementById("chat-form");
const chatInputEl = document.getElementById("chat-input");
const modelSelectEl = document.getElementById("model-select");
const modelCustomInputEl = document.getElementById("model-custom-input");
const effortSelectEl = document.getElementById("effort-select");
const verbositySelectEl = document.getElementById("verbosity-select");
const maxOutputTokensInputEl = document.getElementById("max-output-tokens-input");
const contextMessagesInputEl = document.getElementById("context-messages-input");
const memoryRecallDaysInputEl = document.getElementById("memory-recall-days-input");
const sessionSilenceMSInputEl = document.getElementById("session-silence-ms-input");
const sessionMaxTurnMSInputEl = document.getElementById("session-max-turn-ms-input");
const ttsVoiceSelectEl = document.getElementById("tts-voice-select");
const ttsVoiceCustomInputEl = document.getElementById("tts-voice-custom-input");
const ttsRateInputEl = document.getElementById("tts-rate-input");
const sttStreamingEnabledCheckbox = document.getElementById("stt-streaming-enabled");
const conciseEnabledCheckbox = document.getElementById("concise-enabled");
const sttInterimIntervalMSInputEl = document.getElementById("stt-interim-interval-ms-input");
const sttInterimMinAudioMSInputEl = document.getElementById("stt-interim-min-audio-ms-input");
const configSaveBtn = document.getElementById("config-save");
const configReloadBtn = document.getElementById("config-reload");
const configStatusEl = document.getElementById("config-status");
const modelStatusEl = document.getElementById("model-status");
const diagStatusEl = document.getElementById("diag-status");
const recordBtn = document.getElementById("record-btn");
const stopBtn = document.getElementById("stop-btn");
const voiceStatusEl = document.getElementById("voice-status");
const onlineSearchCheckbox = document.getElementById("online-search");
const showDiagnosticsCheckbox = document.getElementById("show-diagnostics");
const autoTTSCheckbox = document.getElementById("auto-tts");
const consoleLayoutEl = document.getElementById("console-layout");
const controlSidebarEl = document.getElementById("control-sidebar");
const sidebarToggleBtn = document.getElementById("sidebar-toggle");
const deviceSelectEl = document.getElementById("device-select");
const deviceRebootSelectEl = document.getElementById("device-reboot-select");
const sdConfigInputEl = document.getElementById("sd-config-input");
const deviceRefreshBtn = document.getElementById("device-refresh");
const deviceApplyBtn = document.getElementById("device-apply-sd");
const deviceStatusEl = document.getElementById("device-status");
const statusSystemLightEl = document.getElementById("status-light-system");
const statusOAuthLightEl = document.getElementById("status-light-oauth");
const statusConfigLightEl = document.getElementById("status-light-config");
const statusChatLightEl = document.getElementById("status-light-chat");
const statusVoiceLightEl = document.getElementById("status-light-voice");

const diagnosticsStorageKey = "gateway_show_diagnostics";
const sidebarCollapsedStorageKey = "gateway_sidebar_collapsed";
const sdConfigDraftStorageKey = "gateway_sd_config_draft";
const deviceAutoRefreshIntervalMs = 3000;

function buildDefaultSDConfigObject() {
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  const host = String(window.location.host || "").trim();
  const websocket = {
    version: 1,
  };
  if (host) {
    websocket.url = `${proto}://${host}/ws`;
  }

  return {
    websocket,
    conversation: {
      aec_mode: "off",
      continue_listening_after_tts_stop: true,
      post_tts_listen_guard_ms: 300,
      tts_downlink_drain_quiet_ms: 240,
      wake_word_detection_in_listening: false,
      xiaozhi_compat_mode: false,
      mic_send_gate_enabled: true,
    },
    audio: {
      output_volume: 70,
      input_gain: 36,
      input_reference: false,
    },
  };
}

const defaultSDConfigObject = buildDefaultSDConfigObject();

let recorder = null;
let recordStream = null;
let chunks = [];

function setStatusLight(el, state, detail) {
  if (!el) return;
  const normalized = ["idle", "ok", "warn", "err", "busy"].includes(state) ? state : "idle";
  el.setAttribute("data-state", normalized);
  const message = String(detail || "").trim();
  if (message) {
    el.title = message;
    el.setAttribute("aria-label", message);
  } else {
    el.removeAttribute("title");
    el.setAttribute("aria-label", normalized);
  }
}

function setConfigStatus(message, level = "info") {
  if (configStatusEl) {
    configStatusEl.textContent = `Config: ${message}`;
    configStatusEl.classList.remove("ok", "err");
    if (level === "ok") configStatusEl.classList.add("ok");
    if (level === "err") configStatusEl.classList.add("err");
  }
  if (level === "ok") {
    setStatusLight(statusConfigLightEl, "ok", `Config ${message}`);
  } else if (level === "err") {
    setStatusLight(statusConfigLightEl, "err", `Config ${message}`);
  } else {
    setStatusLight(statusConfigLightEl, "warn", `Config ${message}`);
  }
}

function appendMessage(role, text) {
  const div = document.createElement("div");
  div.className = `msg ${role}`;
  div.textContent = text;
  chatLogEl.appendChild(div);
  chatLogEl.scrollTop = chatLogEl.scrollHeight;
}

function setVoiceStatus(message, level = "info") {
  voiceStatusEl.textContent = `Voice: ${message}`;
  voiceStatusEl.classList.remove("ok", "err");
  if (level === "ok") voiceStatusEl.classList.add("ok");
  if (level === "err") voiceStatusEl.classList.add("err");
  if (level === "ok") {
    setStatusLight(statusVoiceLightEl, "ok", `Voice ${message}`);
    return;
  }
  if (level === "err") {
    setStatusLight(statusVoiceLightEl, "err", `Voice ${message}`);
    return;
  }
  const text = String(message || "").toLowerCase();
  if (text.includes("idle")) {
    setStatusLight(statusVoiceLightEl, "idle", `Voice ${message}`);
  } else if (text.includes("recording") || text.includes("processing") || text.includes("playing")) {
    setStatusLight(statusVoiceLightEl, "busy", `Voice ${message}`);
  } else {
    setStatusLight(statusVoiceLightEl, "warn", `Voice ${message}`);
  }
}

function setDeviceStatus(message, level = "info") {
  if (!deviceStatusEl) return;
  deviceStatusEl.textContent = `Device: ${message}`;
  deviceStatusEl.classList.remove("ok", "err");
  if (level === "ok") deviceStatusEl.classList.add("ok");
  if (level === "err") deviceStatusEl.classList.add("err");
}

function diagnosticsEnabled() {
  if (!showDiagnosticsCheckbox) return true;
  return Boolean(showDiagnosticsCheckbox.checked);
}

function setSidebarCollapsed(collapsed) {
  if (!consoleLayoutEl) return;
  consoleLayoutEl.classList.toggle("sidebar-collapsed", collapsed);
  if (controlSidebarEl) {
    controlSidebarEl.setAttribute("data-collapsed", collapsed ? "1" : "0");
  }
  if (sidebarToggleBtn) {
    const label = collapsed ? "Expand sidebar" : "Collapse sidebar";
    sidebarToggleBtn.textContent = collapsed ? "▶" : "◀";
    sidebarToggleBtn.title = label;
    sidebarToggleBtn.setAttribute("aria-label", label);
    sidebarToggleBtn.setAttribute("aria-expanded", collapsed ? "false" : "true");
  }
}

function applySidebarPreference() {
  const stored = String(window.localStorage.getItem(sidebarCollapsedStorageKey) || "").trim();
  setSidebarCollapsed(stored === "1");
}

function setModelStatus(provider, model, effort, online, sessionID, contextMessages, memoryRecallDays) {
  if (!modelStatusEl) return;
  const p = String(provider || "-");
  const m = String(model || "-");
  const e = String(effort || "-");
  const o = String(online ?? "-");
  const sid = String(sessionID || "-");
  const ctx = String(contextMessages ?? "-");
  const recallDays = String(memoryRecallDays ?? "-");
  modelStatusEl.textContent = `Last call: provider=${p} model=${m} effort=${e} context=${ctx} recall_days=${recallDays} online=${o} session=${sid}`;
}

function setDiagnosticsStatus(usage, memoryMessages, sentMessages) {
  if (!diagStatusEl) return;
  if (!diagnosticsEnabled()) {
    diagStatusEl.style.display = "none";
    return;
  }
  diagStatusEl.style.display = "";
  const inputTokens = Number(usage?.input_tokens || 0);
  const outputTokens = Number(usage?.output_tokens || 0);
  const totalTokens = Number(usage?.total_tokens || inputTokens + outputTokens || 0);
  const memory = Number(memoryMessages || 0);
  const sent = Number(sentMessages || 0);
  diagStatusEl.textContent = `Diagnostics: input_tokens=${inputTokens} output_tokens=${outputTokens} total_tokens=${totalTokens} memory_messages=${memory} sent_messages=${sent}`;
}

async function fetchJSON(url, options = {}) {
  const resp = await fetch(url, options);
  const body = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw new Error(body.error || `HTTP ${resp.status}`);
  }
  return body;
}

function fillSelect(el, values, selectedValue) {
  if (!el) return;
  el.innerHTML = "";
  const options = Array.isArray(values) ? values : [];
  const selected = String(selectedValue || "").trim();
  for (const value of options) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = value;
    if (selected && value === selected) {
      option.selected = true;
    }
    el.appendChild(option);
  }
}

function selectedOrFallback(selectEl, fallback = "") {
  if (!selectEl) return fallback;
  const value = String(selectEl.value || "").trim();
  if (value) return value;
  return fallback;
}

function currentModelValue() {
  const custom = String(modelCustomInputEl?.value || "").trim();
  if (custom) return custom;
  return selectedOrFallback(modelSelectEl, "gpt-5.1-codex");
}

function currentVoiceValue() {
  const custom = String(ttsVoiceCustomInputEl?.value || "").trim();
  if (custom) return custom;
  return selectedOrFallback(ttsVoiceSelectEl, "Daniel");
}

function currentContextMessagesValue() {
  const value = Number.parseInt(String(contextMessagesInputEl?.value || "0"), 10);
  if (!Number.isFinite(value) || value <= 0) return 10;
  return Math.min(value, 200);
}

function currentMemoryRecallDaysValue() {
  const value = Number.parseInt(String(memoryRecallDaysInputEl?.value || "0"), 10);
  if (!Number.isFinite(value) || value <= 0) return 30;
  return Math.min(value, 365);
}

function currentMaxOutputTokensValue() {
  const value = Number.parseInt(String(maxOutputTokensInputEl?.value || "0"), 10);
  if (!Number.isFinite(value) || value <= 0) return 500;
  return Math.max(1, Math.min(8192, value));
}

function clampedIntInputValue(inputEl, fallback, min, max) {
  const value = Number.parseInt(String(inputEl?.value || "0"), 10);
  if (!Number.isFinite(value) || value <= 0) return fallback;
  return Math.max(min, Math.min(max, value));
}

function currentSessionSilenceMSValue() {
  return clampedIntInputValue(sessionSilenceMSInputEl, 1200, 200, 10000);
}

function currentSessionMaxTurnMSValue() {
  return clampedIntInputValue(sessionMaxTurnMSInputEl, 15000, 2000, 120000);
}

function currentSTTInterimIntervalMSValue() {
  return clampedIntInputValue(sttInterimIntervalMSInputEl, 900, 200, 10000);
}

function currentSTTInterimMinAudioMSValue() {
  return clampedIntInputValue(sttInterimMinAudioMSInputEl, 1200, 200, 30000);
}

function applyRuntimeConfig(cfg) {
  if (!cfg || typeof cfg !== "object") return;
  const currentModel = String(cfg.model || "gpt-5.1-codex");
  const modelOptions = Array.isArray(cfg.model_options) ? cfg.model_options : [];
  fillSelect(modelSelectEl, modelOptions, currentModel);
  if (modelCustomInputEl) {
    modelCustomInputEl.value = modelOptions.includes(currentModel) ? "" : currentModel;
  }
  if (effortSelectEl) effortSelectEl.value = String(cfg.effort || effortSelectEl.value || "medium");
  if (verbositySelectEl) verbositySelectEl.value = String(cfg.verbosity || verbositySelectEl.value || "medium");
  if (maxOutputTokensInputEl) {
    maxOutputTokensInputEl.value = String(cfg.max_output_tokens || currentMaxOutputTokensValue() || 500);
  }
  if (contextMessagesInputEl) {
    contextMessagesInputEl.value = String(cfg.context_messages || currentContextMessagesValue() || 10);
  }
  if (memoryRecallDaysInputEl) {
    memoryRecallDaysInputEl.value = String(cfg.memory_recall_days || currentMemoryRecallDaysValue() || 30);
  }
  if (sessionSilenceMSInputEl) {
    sessionSilenceMSInputEl.value = String(cfg.session_silence_ms || currentSessionSilenceMSValue() || 1200);
  }
  if (sessionMaxTurnMSInputEl) {
    sessionMaxTurnMSInputEl.value = String(cfg.session_max_turn_ms || currentSessionMaxTurnMSValue() || 15000);
  }
  if (onlineSearchCheckbox) onlineSearchCheckbox.checked = Boolean(cfg.online);
  if (conciseEnabledCheckbox) {
    const concise = typeof cfg.concise === "boolean" ? cfg.concise : conciseEnabledCheckbox.checked;
    conciseEnabledCheckbox.checked = Boolean(concise);
  }
  if (sttStreamingEnabledCheckbox) {
    const enabled = typeof cfg.stt_streaming_enabled === "boolean" ? cfg.stt_streaming_enabled : sttStreamingEnabledCheckbox.checked;
    sttStreamingEnabledCheckbox.checked = Boolean(enabled);
  }
  const currentVoice = String(cfg.tts_voice || "");
  const voiceOptions = Array.isArray(cfg.voice_options) ? cfg.voice_options : [];
  fillSelect(ttsVoiceSelectEl, voiceOptions, currentVoice);
  if (ttsVoiceCustomInputEl) {
    ttsVoiceCustomInputEl.value = voiceOptions.includes(currentVoice) ? "" : currentVoice;
  }
  if (ttsRateInputEl) ttsRateInputEl.value = String(cfg.tts_rate || 220);
  if (sttInterimIntervalMSInputEl) {
    sttInterimIntervalMSInputEl.value = String(cfg.stt_interim_interval_ms || currentSTTInterimIntervalMSValue() || 900);
  }
  if (sttInterimMinAudioMSInputEl) {
    sttInterimMinAudioMSInputEl.value = String(cfg.stt_interim_min_audio_ms || currentSTTInterimMinAudioMSValue() || 1200);
  }
}

function applyDiagnosticsPreference() {
  if (!showDiagnosticsCheckbox) return;
  const stored = String(window.localStorage.getItem(diagnosticsStorageKey) || "").trim();
  if (stored === "0") {
    showDiagnosticsCheckbox.checked = false;
  } else if (stored === "1") {
    showDiagnosticsCheckbox.checked = true;
  }
  setDiagnosticsStatus(null, 0, 0);
}

async function loadRuntimeConfig(forceRefresh = false) {
  try {
    const url = forceRefresh ? "/api/runtime/config?refresh=1" : "/api/runtime/config";
    const cfg = await fetchJSON(url);
    applyRuntimeConfig(cfg);
    setConfigStatus("loaded", "ok");
  } catch (err) {
    setConfigStatus(`load failed: ${err.message}`, "err");
    setStatusLight(statusSystemLightEl, "warn", "Runtime config load failed");
  }
}

async function saveRuntimeConfig() {
  const payload = {
    model: currentModelValue(),
    effort: String(effortSelectEl?.value || "").trim(),
    verbosity: String(verbositySelectEl?.value || "").trim(),
    concise: Boolean(conciseEnabledCheckbox?.checked),
    max_output_tokens: currentMaxOutputTokensValue(),
    context_messages: currentContextMessagesValue(),
    memory_recall_days: currentMemoryRecallDaysValue(),
    session_silence_ms: currentSessionSilenceMSValue(),
    session_max_turn_ms: currentSessionMaxTurnMSValue(),
    stt_streaming_enabled: Boolean(sttStreamingEnabledCheckbox?.checked),
    stt_interim_interval_ms: currentSTTInterimIntervalMSValue(),
    stt_interim_min_audio_ms: currentSTTInterimMinAudioMSValue(),
    online: Boolean(onlineSearchCheckbox?.checked),
    tts_voice: currentVoiceValue(),
    tts_rate: Number.parseInt(String(ttsRateInputEl?.value || "0"), 10) || 0,
  };
  try {
    const cfg = await fetchJSON("/api/runtime/config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    applyRuntimeConfig(cfg);
    setConfigStatus("saved", "ok");
  } catch (err) {
    setConfigStatus(`save failed: ${err.message}`, "err");
  }
}

function initSDConfigEditor() {
  if (!sdConfigInputEl) return;
  const existing = String(sdConfigInputEl.value || "").trim();
  if (existing) return;

  const savedDraft = String(window.localStorage.getItem(sdConfigDraftStorageKey) || "").trim();
  if (savedDraft) {
    try {
      const parsed = JSON.parse(savedDraft);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        sdConfigInputEl.value = JSON.stringify(parsed, null, 2);
        return;
      }
    } catch (_) {
      // ignore invalid local draft and fallback to default template
    }
  }
  sdConfigInputEl.value = JSON.stringify(defaultSDConfigObject, null, 2);
}

async function loadConnectedDevices() {
  if (!deviceSelectEl) return;
  const previousSelection = String(deviceSelectEl.value || "").trim();
  try {
    const data = await fetchJSON("/api/devices");
    const devices = Array.isArray(data.devices) ? data.devices : [];
    deviceSelectEl.innerHTML = "";
    let matchedPreviousSelection = false;
    for (const item of devices) {
      const sessionID = String(item?.session_id || "").trim();
      if (!sessionID) continue;
      const deviceID = String(item?.device_id || "unknown");
      const remoteAddr = String(item?.remote_addr || "-");
      const option = document.createElement("option");
      option.value = sessionID;
      option.textContent = `${deviceID} (${remoteAddr})`;
      if (previousSelection && sessionID === previousSelection) {
        option.selected = true;
        matchedPreviousSelection = true;
      }
      deviceSelectEl.appendChild(option);
    }
    if (devices.length === 0) {
      const option = document.createElement("option");
      option.value = "";
      option.textContent = "No connected devices";
      deviceSelectEl.appendChild(option);
      setDeviceStatus("no connected devices", "err");
      return;
    }
    if (!matchedPreviousSelection && deviceSelectEl.options.length > 0) {
      deviceSelectEl.selectedIndex = 0;
    }
    setDeviceStatus(`loaded ${devices.length} device(s)`, "ok");
  } catch (err) {
    setDeviceStatus(`load failed: ${err.message}`, "err");
  }
}

async function applySDConfigToDevice() {
  const sessionID = String(deviceSelectEl?.value || "").trim();
  if (!sessionID) {
    setDeviceStatus("please choose a connected device", "err");
    return;
  }
  const raw = String(sdConfigInputEl?.value || "").trim();
  if (!raw) {
    setDeviceStatus("config JSON is empty", "err");
    return;
  }

  let configObject = null;
  try {
    configObject = JSON.parse(raw);
  } catch (err) {
    setDeviceStatus(`invalid JSON: ${err.message}`, "err");
    return;
  }
  if (!configObject || typeof configObject !== "object" || Array.isArray(configObject)) {
    setDeviceStatus("config must be a JSON object", "err");
    return;
  }

  // Normalize and persist draft so page refresh won't revert to template.
  const normalized = JSON.stringify(configObject, null, 2);
  if (sdConfigInputEl) sdConfigInputEl.value = normalized;
  window.localStorage.setItem(sdConfigDraftStorageKey, normalized);

  const reboot = String(deviceRebootSelectEl?.value || "false") === "true";
  try {
    await fetchJSON("/api/devices/sdcard-config", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        session_id: sessionID,
        config: configObject,
        reboot,
      }),
    });
    setDeviceStatus(`config queued to ${sessionID}${reboot ? " (reboot requested)" : ""}`, "ok");
  } catch (err) {
    setDeviceStatus(`apply failed: ${err.message}`, "err");
  }
}

async function refreshOAuthStatus() {
  try {
    const info = await fetchJSON("/api/oauth/status");
    const lines = [
      `provider: ${info.provider || "-"}`,
      `authorized: ${Boolean(info.authorized)}`,
      `expired: ${Boolean(info.expired)}`,
      `auth_file: ${info.auth_file || "-"}`,
    ];
    if (info.error) lines.push(`error: ${info.error}`);
    oauthStatusEl.textContent = lines.join("\n");
    if (info.error) {
      setStatusLight(statusOAuthLightEl, "err", `OAuth status error: ${info.error}`);
      setStatusLight(statusSystemLightEl, "err", `OAuth status error: ${info.error}`);
      return;
    }
    const authorized = Boolean(info.authorized) && !Boolean(info.expired);
    if (authorized) {
      setStatusLight(statusOAuthLightEl, "ok", "OAuth ready");
      setStatusLight(statusSystemLightEl, "ok", "Gateway ready");
    } else if (Boolean(info.expired)) {
      setStatusLight(statusOAuthLightEl, "warn", "OAuth token expired. Sign in again.");
      setStatusLight(statusSystemLightEl, "warn", "OAuth expired. Sign in again.");
    } else {
      setStatusLight(statusOAuthLightEl, "warn", "Sign in with GPT OAuth first.");
      setStatusLight(statusSystemLightEl, "warn", "Ready: sign in with OAuth first.");
    }
  } catch (err) {
    oauthStatusEl.textContent = `Failed to fetch status: ${err.message}`;
    setStatusLight(statusOAuthLightEl, "err", `OAuth status failed: ${err.message}`);
    setStatusLight(statusSystemLightEl, "err", `OAuth status failed: ${err.message}`);
  }
}

async function beginOAuth() {
  try {
    setStatusLight(statusOAuthLightEl, "busy", "Starting OAuth flow...");
    const data = await fetchJSON("/oauth2/initiate");
    if (!data.authorize_url) {
      throw new Error("authorize_url missing");
    }
    window.location.href = data.authorize_url;
  } catch (err) {
    setStatusLight(statusOAuthLightEl, "err", `OAuth start failed: ${err.message}`);
    setStatusLight(statusSystemLightEl, "err", `OAuth start failed: ${err.message}`);
  }
}

async function sendTextTurn(text) {
  const value = String(text || "").trim();
  if (!value) return;
  const hasRuntimeControls = Boolean(
    modelSelectEl ||
      modelCustomInputEl ||
      effortSelectEl ||
      verbositySelectEl ||
      onlineSearchCheckbox ||
      conciseEnabledCheckbox,
  );
  const model = currentModelValue();
  const effort = String(effortSelectEl?.value || "").trim();
  const verbosity = String(verbositySelectEl?.value || "").trim();
  const concise = Boolean(conciseEnabledCheckbox?.checked);
  const maxOutputTokens = currentMaxOutputTokensValue();
  const contextMessages = currentContextMessagesValue();
  const memoryRecallDays = currentMemoryRecallDaysValue();
  const online = Boolean(onlineSearchCheckbox?.checked);
  appendMessage("user", value);
  setStatusLight(
    statusChatLightEl,
    "busy",
    hasRuntimeControls
      ? `Sending request: model=${model} effort=${effort} max_output_tokens=${maxOutputTokens} context=${contextMessages} recall_days=${memoryRecallDays} online=${online}`
      : "Sending request with runtime defaults",
  );

  try {
    const payload = { text: value };
    if (modelSelectEl || modelCustomInputEl) payload.model = model;
    if (effortSelectEl) payload.effort = effort;
    if (onlineSearchCheckbox) payload.online = online;
    if (conciseEnabledCheckbox) payload.concise = concise;
    if (maxOutputTokensInputEl) payload.max_output_tokens = maxOutputTokens;
    if (verbositySelectEl) payload.verbosity = verbosity;
    if (contextMessagesInputEl) payload.context_messages = contextMessages;
    if (memoryRecallDaysInputEl) payload.memory_recall_days = memoryRecallDays;

    const data = await fetchJSON("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    setModelStatus(
      data.provider,
      data.model || model,
      data.effort || effort,
      data.online ?? online,
      data.session_id,
      data.context_messages ?? contextMessages,
      data.memory_recall_days ?? memoryRecallDays,
    );
    setDiagnosticsStatus(data.usage || null, data.memory_messages, data.sent_messages);
    const totalTokens = Number(data?.usage?.total_tokens || 0);
    setStatusLight(
      statusChatLightEl,
      "ok",
      `Reply received. session=${data.session_id || "-"} total_tokens=${totalTokens}`,
    );
    setStatusLight(statusSystemLightEl, "ok", "Chat request completed");
    appendMessage("assistant", data.reply || "(empty)");
    if (autoTTSCheckbox.checked && data.reply) {
      await playTTS(data.reply);
    }
  } catch (err) {
    setModelStatus("-", "-", "-", "-", "-", "-", "-");
    setDiagnosticsStatus(null, 0, 0);
    setStatusLight(statusChatLightEl, "err", `Chat failed: ${err.message}`);
    setStatusLight(statusSystemLightEl, "err", `Chat failed: ${err.message}`);
  }
}

async function playTTS(text) {
  const voice = currentVoiceValue();
  const rate = Number.parseInt(String(ttsRateInputEl?.value || "0"), 10) || 0;
  setVoiceStatus("playing...", "info");
  let audioURL = "";
  try {
    const resp = await fetch("/api/tts", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text, voice, rate }),
    });
    if (!resp.ok) {
      const body = await resp.json().catch(() => ({}));
      throw new Error(body.error || `TTS HTTP ${resp.status}`);
    }
    const blob = await resp.blob();
    audioURL = URL.createObjectURL(blob);
    const audio = new Audio(audioURL);
    audio.onended = () => {
      URL.revokeObjectURL(audioURL);
      setVoiceStatus("idle");
    };
    await audio.play();
  } catch (err) {
    if (audioURL) {
      URL.revokeObjectURL(audioURL);
    }
    setVoiceStatus(`tts failed: ${err.message}`, "err");
    throw err;
  }
}

function getPreferredMimeType() {
  const candidates = [
    "audio/webm;codecs=opus",
    "audio/webm",
    "audio/ogg;codecs=opus",
    "audio/ogg",
  ];
  for (const mime of candidates) {
    if (window.MediaRecorder && MediaRecorder.isTypeSupported(mime)) {
      return mime;
    }
  }
  return "";
}

function mergeChannelsToMono(audioBuffer) {
  if (audioBuffer.numberOfChannels === 1) {
    return audioBuffer.getChannelData(0);
  }
  const length = audioBuffer.length;
  const mono = new Float32Array(length);
  for (let i = 0; i < length; i++) {
    let sum = 0;
    for (let c = 0; c < audioBuffer.numberOfChannels; c++) {
      sum += audioBuffer.getChannelData(c)[i];
    }
    mono[i] = sum / audioBuffer.numberOfChannels;
  }
  return mono;
}

function resampleLinear(input, inputRate, outputRate) {
  if (inputRate === outputRate) return input;
  const ratio = inputRate / outputRate;
  const outputLength = Math.max(1, Math.floor(input.length / ratio));
  const output = new Float32Array(outputLength);
  for (let i = 0; i < outputLength; i++) {
    const pos = i * ratio;
    const left = Math.floor(pos);
    const right = Math.min(left + 1, input.length - 1);
    const mix = pos - left;
    output[i] = input[left] * (1 - mix) + input[right] * mix;
  }
  return output;
}

function encodeWav(samples, sampleRate) {
  const bytesPerSample = 2;
  const blockAlign = bytesPerSample;
  const byteRate = sampleRate * blockAlign;
  const dataSize = samples.length * bytesPerSample;
  const buffer = new ArrayBuffer(44 + dataSize);
  const view = new DataView(buffer);

  const writeString = (offset, str) => {
    for (let i = 0; i < str.length; i++) {
      view.setUint8(offset + i, str.charCodeAt(i));
    }
  };

  writeString(0, "RIFF");
  view.setUint32(4, 36 + dataSize, true);
  writeString(8, "WAVE");
  writeString(12, "fmt ");
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, 1, true);
  view.setUint32(24, sampleRate, true);
  view.setUint32(28, byteRate, true);
  view.setUint16(32, blockAlign, true);
  view.setUint16(34, 16, true);
  writeString(36, "data");
  view.setUint32(40, dataSize, true);

  let offset = 44;
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    view.setInt16(offset, s < 0 ? s * 0x8000 : s * 0x7fff, true);
    offset += 2;
  }
  return buffer;
}

async function blobToWav(blob) {
  const context = new (window.AudioContext || window.webkitAudioContext)();
  try {
    const arrayBuffer = await blob.arrayBuffer();
    const decoded = await context.decodeAudioData(arrayBuffer.slice(0));
    const mono = mergeChannelsToMono(decoded);
    const targetRate = 16000;
    const resampled = resampleLinear(mono, decoded.sampleRate, targetRate);
    return new Blob([encodeWav(resampled, targetRate)], { type: "audio/wav" });
  } finally {
    await context.close();
  }
}

async function stopRecordingAndTranscribe() {
  if (!recorder) return;

  await new Promise((resolve) => {
    recorder.onstop = resolve;
    recorder.stop();
  });

  if (recordStream) {
    for (const track of recordStream.getTracks()) track.stop();
  }

  recordBtn.disabled = false;
  stopBtn.disabled = true;

  try {
    setVoiceStatus("processing...");
    const rawBlob = new Blob(chunks, { type: recorder.mimeType || "audio/webm" });
    const wavBlob = await blobToWav(rawBlob);
    const form = new FormData();
    form.append("audio", wavBlob, "speech.wav");

    const sttResp = await fetch("/api/transcribe", { method: "POST", body: form });
    const sttData = await sttResp.json().catch(() => ({}));
    if (!sttResp.ok) {
      throw new Error(sttData.error || `STT HTTP ${sttResp.status}`);
    }
    const text = String(sttData.text || "").trim();
    if (!text) throw new Error("STT returned empty text");

    setVoiceStatus("transcription success", "ok");
    await sendTextTurn(text);
  } catch (err) {
    setVoiceStatus(`failed: ${err.message}`, "err");
  } finally {
    recorder = null;
    recordStream = null;
    chunks = [];
  }
}

async function startRecording() {
  try {
    recordStream = await navigator.mediaDevices.getUserMedia({ audio: true });
    const mimeType = getPreferredMimeType();
    recorder = mimeType ? new MediaRecorder(recordStream, { mimeType }) : new MediaRecorder(recordStream);
    chunks = [];
    recorder.ondataavailable = (event) => {
      if (event.data && event.data.size > 0) chunks.push(event.data);
    };
    recorder.start();
    recordBtn.disabled = true;
    stopBtn.disabled = false;
    setVoiceStatus("recording...");
  } catch (err) {
    setVoiceStatus(`cannot start recording: ${err.message}`, "err");
  }
}

chatFormEl.addEventListener("submit", async (event) => {
  event.preventDefault();
  const value = chatInputEl.value.trim();
  if (!value) return;
  chatInputEl.value = "";
  await sendTextTurn(value);
});

oauthLoginBtn.addEventListener("click", beginOAuth);
oauthRefreshBtn.addEventListener("click", refreshOAuthStatus);
recordBtn.addEventListener("click", startRecording);
stopBtn.addEventListener("click", stopRecordingAndTranscribe);
configSaveBtn?.addEventListener("click", saveRuntimeConfig);
configReloadBtn?.addEventListener("click", () => loadRuntimeConfig(true));
deviceRefreshBtn?.addEventListener("click", loadConnectedDevices);
deviceApplyBtn?.addEventListener("click", applySDConfigToDevice);
sdConfigInputEl?.addEventListener("input", () => {
  const value = String(sdConfigInputEl.value || "").trim();
  if (!value) {
    window.localStorage.removeItem(sdConfigDraftStorageKey);
    return;
  }
  window.localStorage.setItem(sdConfigDraftStorageKey, value);
});
showDiagnosticsCheckbox?.addEventListener("change", () => {
  window.localStorage.setItem(diagnosticsStorageKey, showDiagnosticsCheckbox.checked ? "1" : "0");
  setDiagnosticsStatus(null, 0, 0);
});
sidebarToggleBtn?.addEventListener("click", () => {
  const collapsed = !consoleLayoutEl?.classList.contains("sidebar-collapsed");
  setSidebarCollapsed(Boolean(collapsed));
  window.localStorage.setItem(sidebarCollapsedStorageKey, collapsed ? "1" : "0");
});

applyDiagnosticsPreference();
applySidebarPreference();
setStatusLight(statusSystemLightEl, "warn", "Ready: sign in with OAuth first. Runtime config is in the Runtime Config page.");
setStatusLight(statusOAuthLightEl, "warn", "Sign in with GPT OAuth first.");
setStatusLight(statusConfigLightEl, "idle", "Config not loaded");
setStatusLight(statusChatLightEl, "idle", "No chat sent yet");
setStatusLight(statusVoiceLightEl, "idle", "Voice idle");
loadRuntimeConfig();
refreshOAuthStatus();
initSDConfigEditor();
loadConnectedDevices();
setInterval(loadConnectedDevices, deviceAutoRefreshIntervalMs);
