package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// XrayConfig is a minimal Xray-core configuration for testing a single VLESS key.
type XrayConfig struct {
	Log       map[string]interface{} `json:"log"`
	Inbounds  []XrayInbound          `json:"inbounds"`
	Outbounds []XrayOutbound         `json:"outbounds"`
}

type XrayInbound struct {
	Listen   string         `json:"listen"`
	Port     int            `json:"port"`
	Protocol string         `json:"protocol"`
	Settings map[string]any `json:"settings,omitempty"`
}

type XrayOutbound struct {
	Protocol       string               `json:"protocol"`
	Settings       XrayVlessSettings    `json:"settings"`
	StreamSettings XrayStreamSettings   `json:"streamSettings"`
}

type XrayVlessSettings struct {
	Vnext []XrayVnext `json:"vnext"`
}

type XrayVnext struct {
	Address string      `json:"address"`
	Port    int         `json:"port"`
	Users   []XrayUser  `json:"users"`
}

type XrayUser struct {
	ID         string `json:"id"`
	Flow       string `json:"flow,omitempty"`
	Encryption string `json:"encryption,omitempty"`
}

type XrayStreamSettings struct {
	Network         string                `json:"network"`
	Security        string                `json:"security"`
	XHTTPSettings   *XrayXHTTPSettings    `json:"xhttpSettings,omitempty"`
	RealitySettings *XrayRealitySettings  `json:"realitySettings,omitempty"`
	TLSSettings     *XrayTLSSettings      `json:"tlsSettings,omitempty"`
	WSSettings      *XrayWSSettings       `json:"wsSettings,omitempty"`
	GRPCSettings    *XrayGRPCSettings     `json:"grpcSettings,omitempty"`
	HTTPSettings    *XrayHTTPSettings     `json:"httpSettings,omitempty"`
}

type XrayXHTTPSettings struct {
	Path string           `json:"path,omitempty"`
	Host string           `json:"host,omitempty"`
	Mode string           `json:"mode,omitempty"`
	Extra *XrayXHTTPExtra `json:"extra,omitempty"`
}

type XrayXHTTPExtra struct {
	Headers          map[string]string `json:"headers,omitempty"`
	XPaddingBytes    string            `json:"xPaddingBytes,omitempty"`
	XPaddingHeader   string            `json:"xPaddingHeader,omitempty"`
	XPaddingKey      string            `json:"xPaddingKey,omitempty"`
	XPaddingObfsMode bool              `json:"xPaddingObfsMode,omitempty"`
}

type XrayRealitySettings struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	ServerName  string `json:"serverName,omitempty"`
	PublicKey   string `json:"publicKey,omitempty"`
	ShortID     string `json:"shortId,omitempty"`
	SpiderX     string `json:"spiderX,omitempty"`
}

type XrayTLSSettings struct {
	ServerName string `json:"serverName,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	AllowInsecure bool `json:"allowInsecure,omitempty"`
}

type XrayWSSettings struct {
	Path string `json:"path,omitempty"`
	Host string `json:"host,omitempty"`
}

type XrayGRPCSettings struct {
	ServiceName string `json:"serviceName,omitempty"`
}

type XrayHTTPSettings struct {
	Path string `json:"path,omitempty"`
	Host []string `json:"host,omitempty"`
}

// GenerateXrayConfig creates an Xray-core config for testing a single vless key.
// Used for transports that sing-box does not support natively (xhttp).
func GenerateXrayConfig(key *VlessKey, port int) (*XrayConfig, error) {
	portNum := 443
	if key.Port != "" {
		if _, err := fmt.Sscanf(key.Port, "%d", &portNum); err != nil {
			return nil, fmt.Errorf("invalid port: %s", key.Port)
		}
	}

	stream := XrayStreamSettings{
		Network:  key.Type,
		Security: "none",
	}

	switch key.Security {
	case "reality":
		stream.Security = "reality"
		stream.RealitySettings = &XrayRealitySettings{
			Fingerprint: normalizeFingerprint(key.Fingerprint),
			ServerName:  key.ServerName,
			PublicKey:   key.PublicKey,
			ShortID:     key.ShortID,
			SpiderX:     key.SpiderX,
		}
	case "tls":
		stream.Security = "tls"
		stream.TLSSettings = &XrayTLSSettings{
			ServerName:  key.ServerName,
			Fingerprint: normalizeFingerprint(key.Fingerprint),
			AllowInsecure: key.AllowInsecure == "1",
		}
	}

	switch key.Type {
	case "xhttp":
		stream.XHTTPSettings = &XrayXHTTPSettings{
			// path and spx are separate params: path goes to xhttpSettings,
			// spx goes to realitySettings.spiderX. Do NOT use spx as path.
			Path:  firstNonEmpty(key.Path, "/"),
			Host:  key.Host, // do NOT fall back to sni — server may expect empty host
			Mode:  firstNonEmpty(key.Mode, "auto"),
			Extra: buildXHTTPExtra(key),
		}
	case "ws":
		stream.WSSettings = &XrayWSSettings{
			Path: key.Path,
			Host: key.Host,
		}
	case "grpc":
		stream.GRPCSettings = &XrayGRPCSettings{
			ServiceName: firstNonEmpty(key.ServiceName, key.Path),
		}
	case "http", "httpupgrade":
		stream.HTTPSettings = &XrayHTTPSettings{
			Path: key.Path,
			Host: []string{key.Host},
		}
	}

	cfg := &XrayConfig{
		Log: map[string]interface{}{
			"loglevel": "error",
		},
		Inbounds: []XrayInbound{
			{
				Listen:   "127.0.0.1",
				Port:     port,
				Protocol: "socks",
				Settings: map[string]any{"udp": true},
			},
		},
		Outbounds: []XrayOutbound{
			{
				Protocol: "vless",
				Settings: XrayVlessSettings{
					Vnext: []XrayVnext{
						{
							Address: key.Address,
							Port:    portNum,
							Users: []XrayUser{
								{
									ID:         key.UUID,
									Flow:       key.Flow,
									Encryption: firstNonEmpty(key.Encryption, "none"),
								},
							},
						},
					},
				},
				StreamSettings: stream,
			},
		},
	}

	return cfg, nil
}

// WriteXrayConfig writes the config as JSON to a temp file.
func WriteXrayConfig(cfg *XrayConfig) (string, error) {
	os.MkdirAll("/tmp/vlesssub", 0755)

	if len(cfg.Inbounds) == 0 {
		return "", fmt.Errorf("no inbounds in config")
	}
	port := cfg.Inbounds[0].Port
	filename := filepath.Join("/tmp/vlesssub", fmt.Sprintf("xray-test-%d.json", port))

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}

	return filename, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// buildXHTTPExtra parses the "extra" JSON param from a vless link (headers,
// xPadding anti-DPI settings) plus the standalone x_padding_bytes param.
func buildXHTTPExtra(key *VlessKey) *XrayXHTTPExtra {
	if key.Extra == "" && key.XPaddingBytes == "" {
		return nil
	}

	extra := &XrayXHTTPExtra{}
	if key.Extra != "" {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(key.Extra), &m); err == nil {
			if h, ok := m["headers"].(map[string]interface{}); ok {
				hm := make(map[string]string, len(h))
				for k, v := range h {
					hm[k] = fmt.Sprintf("%v", v)
				}
				extra.Headers = hm
			}
			extra.XPaddingBytes = stringVal(m["xPaddingBytes"])
			extra.XPaddingHeader = stringVal(m["xPaddingHeader"])
			extra.XPaddingKey = stringVal(m["xPaddingKey"])
			if b, ok := m["xPaddingObfsMode"].(bool); ok {
				extra.XPaddingObfsMode = b
			}
		}
	}
	if extra.XPaddingBytes == "" {
		extra.XPaddingBytes = key.XPaddingBytes
	}
	return extra
}

func stringVal(v interface{}) string {
	s, _ := v.(string)
	return s
}

// xrayHostNormalize: xray xhttpSettings.host is a string, not a list.
func normalizeXrayHost(hosts []string) string {
	var out []string
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h != "" {
			out = append(out, h)
		}
	}
	return strings.Join(out, ",")
}
