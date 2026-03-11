package openclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gateway/internal/config"
)

type Transcriber struct {
	root     string
	language string
}

func NewTranscriber(cfg config.Config) *Transcriber {
	return &Transcriber{
		root:     strings.TrimSpace(os.Getenv("GATEWAY_OPENCLAW_ROOT")),
		language: strings.TrimSpace(cfg.STTLanguage),
	}
}

func (t *Transcriber) Transcribe(ctx context.Context, wav []byte) (string, error) {
	if len(wav) == 0 {
		return "", errors.New("stt audio payload is empty")
	}

	root := strings.TrimSpace(t.root)
	if root == "" {
		root = resolveOpenClawInstallRoot()
	}
	if root == "" {
		return "", errors.New("openclaw install root not found (set GATEWAY_OPENCLAW_ROOT)")
	}

	mimeType := normalizeAudioMIME("audio/wav")
	tmp, err := os.CreateTemp("", "gateway-openclaw-stt-*"+extensionFromMIME(mimeType))
	if err != nil {
		return "", fmt.Errorf("create temp audio file failed: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(wav); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temp audio file failed: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp audio file failed: %w", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"node",
		"--input-type=module",
		"-e",
		openClawSTTScript,
		tmpPath,
		mimeType,
		normalizeSTTLanguage(t.language),
	)
	cmd.Env = append(os.Environ(), "OPENCLAW_STT_ROOT="+root)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText == "" {
			errText = err.Error()
		}
		return "", fmt.Errorf("openclaw stt command failed: %s", errText)
	}

	var resp struct {
		OK    bool   `json:"ok"`
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return "", fmt.Errorf(
			"openclaw stt invalid response: %w (stdout=%q stderr=%q)",
			err,
			strings.TrimSpace(stdout.String()),
			strings.TrimSpace(stderr.String()),
		)
	}
	if !resp.OK {
		msg := strings.TrimSpace(resp.Error)
		if msg == "" {
			msg = "openclaw transcription failed"
		}
		return "", errors.New(msg)
	}
	text := strings.TrimSpace(resp.Text)
	if text == "" {
		return "", errors.New("openclaw transcription returned empty text")
	}
	return text, nil
}

func normalizeAudioMIME(raw string) string {
	original := strings.ToLower(strings.TrimSpace(raw))
	if original == "" {
		return "audio/webm"
	}
	s := original
	if idx := strings.IndexByte(s, ';'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	if strings.HasPrefix(s, "audio/") {
		return s
	}
	if s == "application/ogg" {
		return "audio/ogg"
	}
	if strings.Contains(original, "ogg") {
		return "audio/ogg"
	}
	if strings.Contains(original, "wav") {
		return "audio/wav"
	}
	if strings.Contains(original, "mp4") || strings.Contains(original, "m4a") {
		return "audio/mp4"
	}
	return "audio/webm"
}

func normalizeSTTLanguage(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || s == "auto" {
		return ""
	}
	switch s {
	case "zh-cn", "zh-sg", "zh-tw", "zh-hk":
		return "zh"
	case "en-us", "en-gb", "en-au", "en-ca":
		return "en"
	case "ja-jp":
		return "ja"
	case "ko-kr":
		return "ko"
	}
	if idx := strings.IndexByte(s, '-'); idx > 0 {
		return s[:idx]
	}
	return s
}

func extensionFromMIME(mimeType string) string {
	s := strings.ToLower(strings.TrimSpace(mimeType))
	if idx := strings.IndexByte(s, ';'); idx > 0 {
		s = s[:idx]
	}
	switch s {
	case "audio/webm":
		return ".webm"
	case "audio/ogg":
		return ".ogg"
	case "audio/mp4", "audio/m4a":
		return ".m4a"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	default:
		return ".webm"
	}
}

const openClawSTTScript = `
import fs from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const write = (obj) => {
  process.stdout.write(JSON.stringify(obj));
};

const fail = (message) => {
  write({ ok: false, error: String(message || "openclaw stt failed") });
};

const readTextFile = async (filePath) => {
  return await fs.readFile(filePath, "utf8");
};

const parseExportAlias = (source, name) => {
  const aliasMatch = source.match(new RegExp("\\b" + name + "\\s+as\\s+([A-Za-z_$][A-Za-z0-9_$]*)\\b"));
  if (aliasMatch && aliasMatch[1]) {
    return aliasMatch[1];
  }
  const directMatch = source.match(new RegExp("export\\s*\\{[\\s\\S]*?\\b" + name + "\\b[\\s\\S]*?\\}"));
  if (directMatch) {
    return name;
  }
  return "";
};

const listDistModules = async (distDir) => {
  return (await fs.readdir(distDir)).filter((name) => name.endsWith(".js")).sort();
};

const findModuleWithExport = async (distDir, exportName, preferredPrefixes = []) => {
  const entries = await listDistModules(distDir);
  const prioritized = [
    ...entries.filter((name) => preferredPrefixes.some((prefix) => name.startsWith(prefix))),
    ...entries.filter((name) => !preferredPrefixes.some((prefix) => name.startsWith(prefix))),
  ];
  for (const name of prioritized) {
    const fullPath = path.join(distDir, name);
    const source = await readTextFile(fullPath);
    const alias = parseExportAlias(source, exportName);
    if (alias) {
      return { fullPath, name, source, alias };
    }
  }
  return null;
};

const findModuleWithExports = async (distDir, exportNames, preferredPrefixes = []) => {
  const entries = await listDistModules(distDir);
  const prioritized = [
    ...entries.filter((name) => preferredPrefixes.some((prefix) => name.startsWith(prefix))),
    ...entries.filter((name) => !preferredPrefixes.some((prefix) => name.startsWith(prefix))),
  ];
  for (const name of prioritized) {
    const fullPath = path.join(distDir, name);
    const source = await readTextFile(fullPath);
    const aliases = {};
    let ok = true;
    for (const exportName of exportNames) {
      const alias = parseExportAlias(source, exportName);
      if (!alias) {
        ok = false;
        break;
      }
      aliases[exportName] = alias;
    }
    if (ok) {
      return { fullPath, name, source, aliases };
    }
  }
  return null;
};

const summarizeDecision = (decision) => {
  if (!decision || typeof decision !== "object") {
    return "unknown";
  }
  const outcome = typeof decision.outcome === "string" && decision.outcome.trim()
    ? decision.outcome.trim()
    : "unknown";
  const attempts = Array.isArray(decision.attachments)
    ? decision.attachments.flatMap((item) => Array.isArray(item?.attempts) ? item.attempts : [])
    : [];
  const reasons = attempts
    .map((item) => (item && typeof item.reason === "string" ? item.reason.trim() : ""))
    .filter(Boolean);
  if (reasons.length === 0) {
    return outcome;
  }
  return outcome + ": " + reasons[0];
};

const ensureObject = (value) => {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value;
  }
  return {};
};

const hasCommand = (name) => {
  if (!name || typeof name !== "string") {
    return false;
  }
  const out = spawnSync("sh", ["-lc", "command -v " + name + " >/dev/null 2>&1"]);
  return out && out.status === 0;
};

const normalizeMimeType = (raw) => {
  const original = String(raw || "").trim().toLowerCase();
  if (!original) {
    return "audio/webm";
  }
  const base = original.includes(";") ? original.slice(0, original.indexOf(";")).trim() : original;
  if (base.startsWith("audio/")) {
    return base;
  }
  if (base === "application/ogg" || original.includes("ogg")) {
    return "audio/ogg";
  }
  if (original.includes("wav")) {
    return "audio/wav";
  }
  if (original.includes("mp4") || original.includes("m4a")) {
    return "audio/mp4";
  }
  return "audio/webm";
};

try {
  const rawArgs = process.argv.slice(1);
  const hasScriptArg =
    rawArgs.length > 0 &&
    (rawArgs[0].endsWith(".js") || rawArgs[0].endsWith(".mjs") || rawArgs[0].endsWith(".cjs"));
  const args = hasScriptArg ? rawArgs.slice(1) : rawArgs;
  const [audioPath, mimeTypeRaw, languageRaw] = args;
  const root = (process.env.OPENCLAW_STT_ROOT || "").trim();
  const mimeType = normalizeMimeType(mimeTypeRaw);
  const language = (languageRaw || "").trim();
  const resolvedAudioPath = path.isAbsolute(audioPath || "") ? String(audioPath) : path.resolve(String(audioPath || ""));

  if (!resolvedAudioPath) {
    fail("missing audio path");
    process.exit(0);
  }
  if (!root) {
    fail("OPENCLAW_STT_ROOT is empty");
    process.exit(0);
  }
  try {
    const stat = await fs.stat(resolvedAudioPath);
    if (!stat.isFile()) {
      fail("audio path is not a file");
      process.exit(0);
    }
  } catch {
    fail("audio temp file not found");
    process.exit(0);
  }

  const distDir = path.join(root, "dist");
  const runnerModule = await findModuleWithExports(
    distDir,
    ["runCapability", "buildProviderRegistry", "normalizeMediaAttachments", "createMediaAttachmentCache"],
    ["audio-transcription-runner-", "runner-", "pi-embedded-"],
  );
  if (!runnerModule) {
    fail("openclaw dist missing audio transcription runner exports");
    process.exit(0);
  }
  const configModule = await findModuleWithExport(
    distDir,
    "loadConfig",
    ["model-selection-", "auth-profiles-", "config-"],
  );
  if (!configModule) {
    fail("openclaw dist missing loadConfig export");
    process.exit(0);
  }

  const resolveMediaAttachmentLocalRootsAlias = parseExportAlias(runnerModule.source, "resolveMediaAttachmentLocalRoots");
  const runnerMod = await import(pathToFileURL(runnerModule.fullPath).href);
  const configMod = await import(pathToFileURL(configModule.fullPath).href);

  const loadConfig = configMod[configModule.alias];
  const runCapability = runnerMod[runnerModule.aliases.runCapability];
  const buildProviderRegistry = runnerMod[runnerModule.aliases.buildProviderRegistry];
  const normalizeMediaAttachments = runnerMod[runnerModule.aliases.normalizeMediaAttachments];
  const createMediaAttachmentCache = runnerMod[runnerModule.aliases.createMediaAttachmentCache];
  const resolveMediaAttachmentLocalRoots = resolveMediaAttachmentLocalRootsAlias
    ? runnerMod[resolveMediaAttachmentLocalRootsAlias]
    : null;

  if (typeof loadConfig !== "function") {
    fail("openclaw loadConfig function unavailable");
    process.exit(0);
  }
  if (typeof runCapability !== "function") {
    fail("openclaw runCapability function unavailable");
    process.exit(0);
  }
  if (typeof buildProviderRegistry !== "function") {
    fail("openclaw buildProviderRegistry function unavailable");
    process.exit(0);
  }
  if (typeof normalizeMediaAttachments !== "function") {
    fail("openclaw normalizeMediaAttachments function unavailable");
    process.exit(0);
  }
  if (typeof createMediaAttachmentCache !== "function") {
    fail("openclaw createMediaAttachmentCache function unavailable");
    process.exit(0);
  }

  const cfgLoaded = loadConfig();
  const cfg = ensureObject(cfgLoaded);
  cfg.tools = ensureObject(cfg.tools);
  cfg.tools.media = ensureObject(cfg.tools.media);
  cfg.tools.media.audio = ensureObject(cfg.tools.media.audio);
  cfg.tools.media.audio.enabled = cfg.tools.media.audio.enabled !== false;
  if (language && !cfg.tools.media.audio.language) {
    cfg.tools.media.audio.language = language;
  }
  if (!Array.isArray(cfg.tools.media.audio.models) || cfg.tools.media.audio.models.length === 0) {
    if (hasCommand("whisper")) {
      cfg.tools.media.audio.models = [
        {
          type: "cli",
          command: "whisper",
          args: [
            "--model",
            "turbo",
            "--output_format",
            "txt",
            "--output_dir",
            "{{OutputDir}}",
            "--verbose",
            "False",
            "{{MediaPath}}",
          ],
        },
      ];
    }
  }

  const ctx = {
    Body: "",
    RawBody: "",
    CommandBody: "",
    BodyForAgent: "",
    BodyForCommands: "",
    SessionKey: "agent:main:gateway-stt",
    Provider: "webchat",
    Surface: "webchat",
    OriginatingChannel: "webchat",
    ChatType: "direct",
    CommandAuthorized: true,
    MediaPath: resolvedAudioPath,
    MediaType: mimeType,
  };

  const media = normalizeMediaAttachments(ctx);
  if (!Array.isArray(media) || media.length === 0) {
    fail("openclaw runner found no media attachment to transcribe");
    process.exit(0);
  }

  const baseLocalPathRoots =
    typeof resolveMediaAttachmentLocalRoots === "function"
      ? resolveMediaAttachmentLocalRoots({ cfg, ctx })
      : [];
  const localPathRoots = Array.isArray(baseLocalPathRoots) ? baseLocalPathRoots.slice() : [];
  const tempAudioDir = path.dirname(resolvedAudioPath);
  if (tempAudioDir) {
    localPathRoots.push(tempAudioDir);
    try {
      localPathRoots.push(await fs.realpath(tempAudioDir));
    } catch {}
  }
  const cache = createMediaAttachmentCache(media, { localPathRoots });

  let transcript = "";
  let decision = null;
  try {
    const result = await runCapability({
      capability: "audio",
      cfg,
      ctx,
      attachments: cache,
      media,
      providerRegistry: buildProviderRegistry(),
      config: cfg.tools.media.audio,
    });
    decision = result?.decision ?? null;
    const outputs = Array.isArray(result?.outputs) ? result.outputs : [];
    const first = outputs.find(
      (item) =>
        item &&
        item.kind === "audio.transcription" &&
        typeof item.text === "string" &&
        item.text.trim().length > 0,
    );
    transcript = first && typeof first.text === "string" ? first.text.trim() : "";
  } finally {
    if (cache && typeof cache.cleanup === "function") {
      await cache.cleanup().catch(() => {});
    }
  }

  if (!transcript) {
    const summary = summarizeDecision(decision);
    fail(
      "openclaw transcription returned empty result (" +
        summary +
        "). Configure tools.media.audio.models or install local whisper/whisper-cli.",
    );
    process.exit(0);
  }

  write({ ok: true, text: transcript });
} catch (err) {
  const message =
    err && typeof err === "object" && "message" in err ? err.message : String(err);
  fail(message);
}
`
