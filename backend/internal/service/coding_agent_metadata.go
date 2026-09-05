package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	maxCodingAgentIDLength       = 255
	maxCodingAgentOriginatorSize = 255
	maxCodexTurnMetadataSize     = 4096
)

// CodingAgentMetadata contains allowlisted client/device identifiers that coding
// agents already send. It is request metadata only: prompt, messages, input, tool
// payloads and response text are intentionally never parsed or persisted here.
type CodingAgentMetadata struct {
	ClientMachineID     string
	ClientMachineSource string
	ClientDeviceID      string
	ClientAccountUUID   string
	ClientOriginator    string
	CodexInstallationID string
	CodexWindowID       string
	CodexSessionID      string
	CodexThreadID       string
	CodexTurnID         string
	TerminalHash        string
}

func (m CodingAgentMetadata) Empty() bool {
	return m.ClientMachineID == "" &&
		m.ClientMachineSource == "" &&
		m.ClientDeviceID == "" &&
		m.ClientAccountUUID == "" &&
		m.ClientOriginator == "" &&
		m.CodexInstallationID == "" &&
		m.CodexWindowID == "" &&
		m.CodexSessionID == "" &&
		m.CodexThreadID == "" &&
		m.CodexTurnID == "" &&
		m.TerminalHash == ""
}

// ExtractCodingAgentMetadata extracts stable client/device identifiers from
// headers and allowlisted JSON paths in the request body. The values are captured
// before async usage recording so the usage worker does not access gin.Context.
func ExtractCodingAgentMetadata(c *gin.Context, body []byte, clientIP string) CodingAgentMetadata {
	if c == nil || c.Request == nil {
		return CodingAgentMetadata{}
	}

	meta := CodingAgentMetadata{
		ClientOriginator: sanitizeCodingAgentValue(c.GetHeader("Originator"), maxCodingAgentOriginatorSize),
	}

	if machineID := firstSanitizedHeader(c, "X-Client-Machine", "X-Client-Machine-ID"); machineID != "" {
		meta.ClientMachineID = machineID
		meta.ClientMachineSource = "x_client_machine"
	}

	if installationID := sanitizeCodingAgentValue(c.GetHeader("x-codex-installation-id"), maxCodingAgentIDLength); installationID != "" {
		meta.CodexInstallationID = installationID
	}
	if windowID := sanitizeCodingAgentValue(c.GetHeader("x-codex-window-id"), maxCodingAgentIDLength); windowID != "" {
		meta.CodexWindowID = windowID
	}
	applyCodexTurnMetadata(&meta, c.GetHeader("x-codex-turn-metadata"))

	if len(body) > 0 {
		if parsed := ParseMetadataUserID(gjson.GetBytes(body, "metadata.user_id").String()); parsed != nil {
			meta.ClientDeviceID = sanitizeCodingAgentValue(parsed.DeviceID, maxCodingAgentIDLength)
			meta.ClientAccountUUID = sanitizeCodingAgentValue(parsed.AccountUUID, maxCodingAgentIDLength)
			if meta.CodexSessionID == "" {
				meta.CodexSessionID = sanitizeCodingAgentValue(parsed.SessionID, maxCodingAgentIDLength)
			}
		}

		if installationID := sanitizeCodingAgentValue(gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(), maxCodingAgentIDLength); installationID != "" && meta.CodexInstallationID == "" {
			meta.CodexInstallationID = installationID
		}
		if windowID := sanitizeCodingAgentValue(gjson.GetBytes(body, "client_metadata.x-codex-window-id").String(), maxCodingAgentIDLength); windowID != "" && meta.CodexWindowID == "" {
			meta.CodexWindowID = windowID
		}
		if value := sanitizeCodingAgentValue(gjson.GetBytes(body, "client_metadata.installation_id").String(), maxCodingAgentIDLength); value != "" && meta.CodexInstallationID == "" {
			meta.CodexInstallationID = value
		}
		if value := sanitizeCodingAgentValue(gjson.GetBytes(body, "client_metadata.window_id").String(), maxCodingAgentIDLength); value != "" && meta.CodexWindowID == "" {
			meta.CodexWindowID = value
		}
		if value := sanitizeCodingAgentValue(gjson.GetBytes(body, "client_metadata.session_id").String(), maxCodingAgentIDLength); value != "" && meta.CodexSessionID == "" {
			meta.CodexSessionID = value
		}
		if value := sanitizeCodingAgentValue(gjson.GetBytes(body, "client_metadata.thread_id").String(), maxCodingAgentIDLength); value != "" && meta.CodexThreadID == "" {
			meta.CodexThreadID = value
		}
		if value := sanitizeCodingAgentValue(gjson.GetBytes(body, "client_metadata.turn_id").String(), maxCodingAgentIDLength); value != "" && meta.CodexTurnID == "" {
			meta.CodexTurnID = value
		}
		applyCodexTurnMetadata(&meta, gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String())
	}

	if meta.ClientMachineID == "" {
		switch {
		case meta.CodexInstallationID != "":
			meta.ClientMachineID = meta.CodexInstallationID
			meta.ClientMachineSource = "codex_installation"
		case meta.ClientDeviceID != "":
			meta.ClientMachineID = meta.ClientDeviceID
			meta.ClientMachineSource = "metadata_device"
		}
	}
	meta.TerminalHash = deriveTerminalHash(meta, c.GetHeader("User-Agent"), clientIP)
	return meta
}

func firstSanitizedHeader(c *gin.Context, names ...string) string {
	for _, name := range names {
		if value := sanitizeCodingAgentValue(c.GetHeader(name), maxCodingAgentIDLength); value != "" {
			return value
		}
	}
	return ""
}

func applyCodexTurnMetadata(meta *CodingAgentMetadata, raw string) {
	if meta == nil {
		return
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxCodexTurnMetadataSize || !json.Valid([]byte(raw)) {
		return
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return
	}
	setString := func(dst *string, key string) {
		if *dst != "" {
			return
		}
		value, _ := decoded[key].(string)
		*dst = sanitizeCodingAgentValue(value, maxCodingAgentIDLength)
	}
	setString(&meta.CodexInstallationID, "installation_id")
	setString(&meta.CodexSessionID, "session_id")
	setString(&meta.CodexThreadID, "thread_id")
	setString(&meta.CodexTurnID, "turn_id")
	setString(&meta.CodexWindowID, "window_id")
}

func sanitizeCodingAgentValue(raw string, maxRunes int) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	count := 0
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return ""
		}
		count++
		if count > maxRunes {
			return ""
		}
	}
	return trimmed
}

func deriveTerminalHash(meta CodingAgentMetadata, userAgent, clientIP string) string {
	if meta.ClientMachineID != "" && meta.ClientMachineSource != "" {
		return sha256Hex(meta.ClientMachineSource + "\x00" + meta.ClientMachineID)
	}
	ua := sanitizeCodingAgentValue(userAgent, 512)
	ipBucket := clientIPBucket(clientIP)
	if ua == "" && ipBucket == "" {
		return ""
	}
	return sha256Hex("ua_ip_weak\x00" + ua + "\x00" + ipBucket)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func clientIPBucket(raw string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if addr.Is4() {
		prefix, _ := addr.Prefix(24)
		return prefix.String()
	}
	prefix, _ := addr.Prefix(64)
	return prefix.String()
}
