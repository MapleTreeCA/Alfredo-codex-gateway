package openclaw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gateway/internal/audio"
	"gateway/internal/config"
)

type SpeechSynthesizer struct {
	root string
}

func NewSpeechSynthesizer(cfg config.Config) *SpeechSynthesizer {
	_ = cfg
	return &SpeechSynthesizer{
		root: strings.TrimSpace(os.Getenv("GATEWAY_OPENCLAW_ROOT")),
	}
}

func (s *SpeechSynthesizer) Synthesize(ctx context.Context, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("tts input is empty")
	}

	root := strings.TrimSpace(s.root)
	if root == "" {
		root = resolveOpenClawInstallRoot()
	}
	if root == "" {
		return nil, errors.New("openclaw install root not found (set GATEWAY_OPENCLAW_ROOT)")
	}

	cmd := exec.CommandContext(
		ctx,
		"node",
		"--input-type=module",
		"-e",
		openClawTTSScript,
		text,
	)
	cmd.Env = append(os.Environ(), "OPENCLAW_TTS_ROOT="+root)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText == "" {
			errText = err.Error()
		}
		return nil, fmt.Errorf("openclaw tts command failed: %s", errText)
	}

	var resp struct {
		OK          bool   `json:"ok"`
		AudioBase64 string `json:"audioBase64"`
		SampleRate  int    `json:"sampleRate"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf(
			"openclaw tts invalid response: %w (stdout=%q stderr=%q)",
			err,
			strings.TrimSpace(stdout.String()),
			strings.TrimSpace(stderr.String()),
		)
	}
	if !resp.OK {
		message := strings.TrimSpace(resp.Error)
		if message == "" {
			message = "openclaw tts failed"
		}
		return nil, errors.New(message)
	}
	if resp.SampleRate <= 0 {
		return nil, errors.New("openclaw tts returned invalid sample rate")
	}

	pcm, err := base64.StdEncoding.DecodeString(strings.TrimSpace(resp.AudioBase64))
	if err != nil {
		return nil, fmt.Errorf("decode openclaw tts audio failed: %w", err)
	}
	return pcm16ToWAV(resp.SampleRate, pcm)
}

func pcm16ToWAV(sampleRate int, pcm []byte) ([]byte, error) {
	if sampleRate <= 0 {
		return nil, errors.New("sample rate must be > 0")
	}
	if len(pcm) == 0 {
		return nil, errors.New("pcm payload is empty")
	}
	if len(pcm)%2 != 0 {
		return nil, errors.New("pcm payload has odd byte length")
	}

	samples := make([]int16, len(pcm)/2)
	if err := binary.Read(bytes.NewReader(pcm), binary.LittleEndian, &samples); err != nil {
		return nil, fmt.Errorf("read pcm payload failed: %w", err)
	}
	return audio.EncodeWAV(sampleRate, samples)
}

func resolveOpenClawInstallRoot() string {
	candidates := make([]string, 0, 4)

	if binaryPath, err := exec.LookPath("openclaw"); err == nil {
		candidates = append(candidates, filepath.Dir(binaryPath))
		if resolved, err := filepath.EvalSymlinks(binaryPath); err == nil {
			candidates = append(candidates, filepath.Dir(resolved))
			candidates = append(candidates, filepath.Clean(filepath.Join(filepath.Dir(resolved), "..")))
		}
	}

	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if hasOpenClawDist(dir) {
			return dir
		}
	}
	return ""
}

func hasOpenClawDist(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(root, "dist"))
	return err == nil && info.IsDir()
}

const openClawTTSScript = `
import fs from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";

const write = (obj) => {
  process.stdout.write(JSON.stringify(obj));
};

const fail = (message) => {
  write({ ok: false, error: String(message || "openclaw tts failed") });
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
      return { fullPath, alias, name };
    }
  }
  return null;
};

const transcodeFileToPCM = (inputPath, sampleRate) => {
  const result = spawnSync(
    "ffmpeg",
    [
      "-v",
      "error",
      "-i",
      inputPath,
      "-f",
      "s16le",
      "-acodec",
      "pcm_s16le",
      "-ac",
      "1",
      "-ar",
      String(sampleRate),
      "pipe:1",
    ],
    { encoding: null, maxBuffer: 32 * 1024 * 1024 },
  );
  if (result.status !== 0 || !result.stdout || result.stdout.length === 0) {
    const stderr = result.stderr ? String(result.stderr).trim() : "";
    throw new Error(stderr || "ffmpeg pcm transcode failed");
  }
  return Buffer.from(result.stdout);
};

try {
  const rawArgs = process.argv.slice(1);
  const hasScriptArg =
    rawArgs.length > 0 &&
    (rawArgs[0].endsWith(".js") || rawArgs[0].endsWith(".mjs") || rawArgs[0].endsWith(".cjs"));
  const args = hasScriptArg ? rawArgs.slice(1) : rawArgs;
  const [textRaw] = args;
  const text = String(textRaw || "").trim();
  const root = String(process.env.OPENCLAW_TTS_ROOT || "").trim();

  if (!text) {
    fail("missing tts text");
    process.exit(0);
  }
  if (!root) {
    fail("OPENCLAW_TTS_ROOT is empty");
    process.exit(0);
  }

  const distDir = path.join(root, "dist");
  const ttsModule = await findModuleWithExport(
    distDir,
    "textToSpeechTelephony",
    ["reply-", "compact-", "pi-embedded-"],
  );
  const configModule = await findModuleWithExport(
    distDir,
    "loadConfig",
    ["model-selection-", "auth-profiles-", "config-"],
  );
  if (!configModule) {
    fail("openclaw dist missing loadConfig export");
    process.exit(0);
  }

  const configMod = await import(pathToFileURL(configModule.fullPath).href);
  const loadConfig = configMod[configModule.alias];
  if (typeof loadConfig !== "function") {
    fail("openclaw loadConfig unavailable");
    process.exit(0);
  }

  const cfg = loadConfig();
  if (ttsModule) {
    const ttsMod = await import(pathToFileURL(ttsModule.fullPath).href);
    const textToSpeechTelephony = ttsMod[ttsModule.alias];
    if (typeof textToSpeechTelephony === "function") {
      const result = await textToSpeechTelephony({ text, cfg });
      if (!result || result.success !== true || !result.audioBuffer) {
        fail(result && result.error ? result.error : "openclaw telephony tts failed");
        process.exit(0);
      }
      if (!result.sampleRate || Number(result.sampleRate) <= 0) {
        fail("openclaw telephony tts returned invalid sample rate");
        process.exit(0);
      }

      const audioBuffer = Buffer.from(result.audioBuffer);
      write({
        ok: true,
        audioBase64: audioBuffer.toString("base64"),
        sampleRate: Number(result.sampleRate),
      });
      process.exit(0);
    }
  }

  const fileTTSModule = await findModuleWithExport(distDir, "textToSpeech");
  if (!fileTTSModule) {
    fail("openclaw dist missing textToSpeech export");
    process.exit(0);
  }
  const fileTTSMod = await import(pathToFileURL(fileTTSModule.fullPath).href);
  const textToSpeech = fileTTSMod[fileTTSModule.alias];
  if (typeof textToSpeech !== "function") {
    fail("openclaw textToSpeech unavailable");
    process.exit(0);
  }

  const result = await textToSpeech({ text, cfg });
  if (!result || result.success !== true || !result.audioPath) {
    fail(result && result.error ? result.error : "openclaw file tts failed");
    process.exit(0);
  }

  const sampleRate = 24000;
  const audioBuffer = transcodeFileToPCM(result.audioPath, sampleRate);
  write({
    ok: true,
    audioBase64: audioBuffer.toString("base64"),
    sampleRate,
  });
} catch (err) {
  const message =
    err && typeof err === "object" && "message" in err ? err.message : String(err);
  fail(message);
}
`
