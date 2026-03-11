# 🤖 Alfred(o) Project Development Roadmap

## 📝 Project Overview
**Alfredo** is a hardware agent built on the **ESP32** chip.
* **Core Brain**: Supported by the `brain-gateway`.
* **Main Capabilities**: Voice interaction (STT/TTS), facial expressions, vision, and head/limb articulation.
* **Alfred (Web Version)**: A digital twin of Alfredo, featuring all capabilities except for physical movement and hardware expansion.

---

## 🗓️ Completed Milestones (2026)

| Status | Timeline | Task Description |
| :---: | :--- | :--- |
| [✅] | **Jan** | Hardware selection and component sourcing. |
| [✅] | **Mid-Feb** | Developed Alfred desktop software and debugged backend. |
| [✅] | **Early Mar** | Created the initial Brain Gateway. |
| [✅] | **Early Mar** | Alfredo hardware arrived. |
| [✅] | **Early Mar** | Developed initial firmware for ESP32 (based on the Xiaozhi template). |
| [✅] | **Mar 8** | Implemented expression response system. |
| [✅] | **Mar 10** | Built the primary brain module: `codex-gateway`. |
| [✅] | **Mar 10** | Completed `codex-gateway` memory bank and local STT/TTS modules. |

---

## 🚀 Future Roadmap

### 🔊 Phase 1: Interaction Optimization & Communication
* [❌] **Voice Stream Integration**: Connect Alfredo to `codex-gateway`; ensure smooth WebSocket voice collection and STT workflows.
* [❌] **Conversation Flow**: Debug TTS playback, session intervals, silence detection, and end-of-turn logic for natural chatting.
* [❌] **Expression Migration**: Move expression control to the gateway to trigger animations based on dialogue context.
* [❌] **Animation Refinement**: Implement smooth transitions and animations for expression sets.

### 🧠 Phase 2: Brain Enhancement & Skill Expansion
* [❌] **Performance Tuning**: Implement Token throttling, memory compression, and optimized search for faster response times.
* [❌] **Skill Modules**: Enable local memo searching, calendar queries, and basic Gmail reading via the gateway.
* [❌] **Vision Capabilities**: Enable camera-based face tracking and person recognition (prioritizing local models to save tokens).

### ⚙️ Phase 3: Hardware Integration & Physical Presence
* [❌] **Servo Calibration**: Debug two 9g servo motors to enable physical face-tracking movement.
* [❌] **3D Structural Design**: Design and print the chassis to secure the motherboard, motors, and brackets.

### 🌐 Phase 4: Advanced AI & Cloud Deployment
* [❌] **Deep MCP Integration**: Develop MCP environments for complex tasks like searching/negotiating on second-hand marketplaces.
* [❌] **Programming Workflow**: Build a coding MCP environment so Alfredo can execute and report on tasks via voice commands.
* [❌] **Mobility & Cloud**: Add a GSM module for portable use; deploy the gateway to the cloud with secure OAuth storage.
* [❌] **Multi-Terminal Support**: Support multiple hardware units and web clones for non-technical family members to use.

---

## 🐞 Debug Notes (2026-03-11)
* [⚠️] **Firmware Connection Target Mismatch**: After flashing `m5stack-core-s3`, device connected to `mqtt.xiaozhi.me` instead of private gateway.
* [🔍] **Evidence**: Boot log shows `MQTT: Connecting to endpoint mqtt.xiaozhi.me`.
* [🔍] **Current Config State**:
  `CONFIG_OTA_URL="https://api.tenclass.net/xiaozhi/ota/"`, `CONFIG_BOOT_DEFAULT_WEBSOCKET_URL=""`, and `CONFIG_SKIP_OTA_IF_BOOT_DEFAULT_WEBSOCKET` is disabled.
* [🧭] **Root Cause**: Device still follows OTA activation path and receives Xiaozhi cloud protocol config (MQTT), so it does not bind to local gateway.
* [➡️] **Next Step**: Force direct gateway boot path (set boot websocket URL/token and enable skip-OTA for local integration testing).
* [✅] **Protocol Priority Fix Applied**:
  `main/application.cc` now prioritizes local websocket config (`runtime`/`persisted`/`boot_default`) over OTA MQTT when selecting protocol.
* [✅] **OTA Bypass Hardened**:
  `main/ota.cc` now skips OTA not only for runtime websocket overrides, but also when persisted `websocket.url` already exists.
* [✅] **CoreS3 Gateway Boot Defaults Enabled**:
  `main/boards/m5stack-core-s3/config.json` now sets:
  `CONFIG_SKIP_OTA_IF_BOOT_DEFAULT_WEBSOCKET=y` and
  `CONFIG_BOOT_DEFAULT_WEBSOCKET_URL="ws://10.0.0.175:18910/ws"`.
* [✅] **Flash + Runtime Verification**:
  Device log now shows:
  `Application: Protocol select: websocket (local config runtime=0 persisted=1 boot_default=1)`,
  `WS: Using boot default websocket config`,
  `WS: Connecting to websocket server: ws://10.0.0.175:18910/ws`.
* [✅] **End-to-End Connectivity Confirmed**:
  After starting gateway with local modules (`go run ./cmd/codex-gateway`),
  gateway log shows:
  `session=... connected remote=10.0.0.16 ... protocol=1`.
