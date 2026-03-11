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
const onlineSearchCheckbox = document.getElementById("online-search");
const conciseEnabledCheckbox = document.getElementById("concise-enabled");
const sttStreamingEnabledCheckbox = document.getElementById("stt-streaming-enabled");
const sttInterimIntervalMSInputEl = document.getElementById("stt-interim-interval-ms-input");
const sttInterimMinAudioMSInputEl = document.getElementById("stt-interim-min-audio-ms-input");
const configSaveBtn = document.getElementById("config-save");
const configReloadBtn = document.getElementById("config-reload");
const configStatusEl = document.getElementById("config-status");

function setConfigStatus(message, level = "info") {
  if (!configStatusEl) return;
  configStatusEl.textContent = `Config: ${message}`;
  configStatusEl.classList.remove("ok", "err");
  if (level === "ok") configStatusEl.classList.add("ok");
  if (level === "err") configStatusEl.classList.add("err");
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

function clampedIntInputValue(inputEl, fallback, min, max) {
  const value = Number.parseInt(String(inputEl?.value || "0"), 10);
  if (!Number.isFinite(value) || value <= 0) return fallback;
  return Math.max(min, Math.min(max, value));
}

function currentMaxOutputTokensValue() {
  return clampedIntInputValue(maxOutputTokensInputEl, 500, 1, 8192);
}

function currentContextMessagesValue() {
  return clampedIntInputValue(contextMessagesInputEl, 10, 1, 200);
}

function currentMemoryRecallDaysValue() {
  return clampedIntInputValue(memoryRecallDaysInputEl, 30, 1, 365);
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

async function loadRuntimeConfig(forceRefresh = false) {
  try {
    const url = forceRefresh ? "/api/runtime/config?refresh=1" : "/api/runtime/config";
    const cfg = await fetchJSON(url);
    applyRuntimeConfig(cfg);
    setConfigStatus("loaded", "ok");
  } catch (err) {
    setConfigStatus(`load failed: ${err.message}`, "err");
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

configSaveBtn?.addEventListener("click", saveRuntimeConfig);
configReloadBtn?.addEventListener("click", () => loadRuntimeConfig(true));

loadRuntimeConfig();
