package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SingBoxConfig represents a sing-box configuration.
type SingBoxConfig struct {
	Log       LogConfig   `json:"log"`
	Inbounds  []Inbound   `json:"inbounds"`
	Outbounds []Outbound  `json:"outbounds"`
	Route     RouteConfig `json:"route,omitempty"`
}

type LogConfig struct {
	Level  string `json:"level"`
	Output string `json:"output"`
}

type Inbound struct {
	Type       string `json:"type"`
	Tag        string `json:"tag"`
	Listen     string `json:"listen"`
	ListenPort int    `json:"listen_port"`
}

type Outbound struct {
	Tag      string `json:"tag"`
	Type     string `json:"type"`
	Server   string `json:"server,omitempty"`
	Port     int    `json:"server_port,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Network  string `json:"network,omitempty"`

	// TLS (includes reality)
	TLS *TLSConfig `json:"tls,omitempty"`

	// Transport fields
	Transport *TransportConfig `json:"transport,omitempty"`

	// Multiplex
	Multiplex *MultiplexConfig `json:"multiplex,omitempty"`

	// Packet encoding
	PacketEncoding string `json:"packet_encoding,omitempty"`
}

type TLSConfig struct {
	Enabled    bool        `json:"enabled"`
	ServerName string      `json:"server_name,omitempty"`
	Insecure   bool        `json:"insecure,omitempty"`
	UTLS       *UTLSConfig `json:"utls,omitempty"`
	Alpn       []string    `json:"alpn,omitempty"`
	Reality    *RealityConfig `json:"reality,omitempty"`
}

type UTLSConfig struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type RealityConfig struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key,omitempty"`
	ShortID   string `json:"short_id,omitempty"`
}

type TransportConfig struct {
	Type                 string              `json:"type,omitempty"`
	Path                 string              `json:"path,omitempty"`
	Headers              map[string][]string `json:"headers,omitempty"`
	ServiceName          string              `json:"service_name,omitempty"`
	IdleTimeout          string              `json:"idle_timeout,omitempty"`
	PingTimeout          string              `json:"ping_timeout,omitempty"`
	PermitWithoutStream  bool                `json:"permit_without_stream,omitempty"`
}

type MultiplexConfig struct {
	Enabled        bool   `json:"enabled"`
	Protocol       string `json:"protocol,omitempty"`
	MaxConnections int    `json:"max_connections,omitempty"`
}

type RouteConfig struct {
	Rules               []RouteRule `json:"rules"`
	AutoDetectInterface bool        `json:"auto_detect_interface"`
	Final               string      `json:"final"`
}

type RouteRule struct{}

// GenerateConfig creates a sing-box config for testing a single vless key.
func GenerateConfig(key *VlessKey, port int) (*SingBoxConfig, error) {
	outbound := Outbound{
		Tag:     "proxy",
		Type:    "vless",
		Server:  key.Address,
		UUID:    key.UUID,
		Flow:    key.Flow,
	}

	// Parse port
	portNum := 443
	if key.Port != "" {
		if _, err := fmt.Sscanf(key.Port, "%d", &portNum); err != nil {
			return nil, fmt.Errorf("invalid port: %s", key.Port)
		}
	}
	outbound.Port = portNum

	// Network: sing-box accepts only tcp/udp here (transport type goes to Transport below)
	outbound.Network = "tcp"

	// Packet encoding
	if key.PacketEncoding != "" {
		outbound.PacketEncoding = key.PacketEncoding
	}

	// Security: reality or tls
	if key.Security == "reality" {
		realityCfg := &RealityConfig{
			Enabled:   true,
			PublicKey: key.PublicKey,
			ShortID:   key.ShortID,
		}
		outbound.TLS = &TLSConfig{
			Enabled:    true,
			ServerName: key.ServerName,
			UTLS: &UTLSConfig{
				Enabled:     true,
				Fingerprint: normalizeFingerprint(key.Fingerprint),
			},
			Reality: realityCfg,
		}
	} else if key.Security == "tls" {
		serverName := key.ServerName
		// Some panels issue certs for the server IP only and omit sni in the
		// link (host= is an HTTP header, not TLS SNI). Fall back to the server
		// address so TLS verification succeeds without allowInsecure.
		if serverName == "" && key.Address != "" {
			serverName = key.Address
		}
		outbound.TLS = &TLSConfig{
			Enabled:    true,
			ServerName: serverName,
			Insecure:   key.AllowInsecure == "1",
			UTLS: &UTLSConfig{
				Enabled:     true,
				Fingerprint: normalizeFingerprint(key.Fingerprint),
			},
		}
		// ALPN for tls+flow
		if outbound.Flow != "" {
			outbound.TLS.Alpn = []string{"h2", "http/1.1"}
		}
	}

	// Transport for non-tcp. NOTE: sing-box has no "xhttp" transport type —
	// XHTTP/split-http is the "http" transport (host/path/headers).
	if key.Type != "tcp" && key.Type != "" {
		transportType := key.Type
		if transportType == "xhttp" {
			transportType = "http"
		}
		transport := &TransportConfig{
			Type: transportType,
		}
		if key.Type == "grpc" {
			if key.ServiceName != "" {
				transport.ServiceName = key.ServiceName
			} else if key.Path != "" {
				transport.ServiceName = key.Path
			}
		} else if key.Path != "" {
			transport.Path = key.Path
		} else if key.Type == "xhttp" && key.SpiderX != "" {
			transport.Path = key.SpiderX
		}
		// NOTE: sing-box has no top-level "host" field in transport config —
		// Host header goes into headers only. Setting both breaks config decode.
		if key.Host != "" {
			transport.Headers = map[string][]string{
				"Host": {key.Host},
			}
		} else if key.Type == "xhttp" && key.ServerName != "" {
			transport.Headers = map[string][]string{
				"Host": {key.ServerName},
			}
		}
		outbound.Transport = transport
	}

	// Multiplex for stream-based transports (xhttp/split-http multiplexes itself)
	if key.Type == "grpc" || key.Type == "httpupgrade" || key.Type == "h2" {
		outbound.Multiplex = &MultiplexConfig{
			Enabled:  true,
			Protocol: "smux",
		}
	}

	// Default uTLS fingerprint
	if outbound.TLS != nil && outbound.TLS.UTLS != nil && outbound.TLS.UTLS.Fingerprint == "" {
		outbound.TLS.UTLS.Fingerprint = "chrome"
	}

	cfg := &SingBoxConfig{
		Log: LogConfig{
			Level:  "error",
			Output: fmt.Sprintf("/tmp/vlesssub/sing-box-%d.log", port),
		},
		Inbounds: []Inbound{
			{
				Type:       "socks",
				Tag:        fmt.Sprintf("socks-in-%d", port),
				Listen:     "127.0.0.1",
				ListenPort: port,
			},
		},
		Outbounds: []Outbound{
			outbound,
			{
				Type: "direct",
				Tag:  "direct",
			},
		},
		Route: RouteConfig{
			Rules:               []RouteRule{},
			AutoDetectInterface: true,
			Final:               "proxy",
		},
	}

	return cfg, nil
}

// WriteConfig writes the config as JSON to a temp file.
func WriteConfig(cfg *SingBoxConfig) (string, error) {
	os.MkdirAll("/tmp/vlesssub", 0755)

	if len(cfg.Inbounds) == 0 {
		return "", fmt.Errorf("no inbounds in config")
	}
	port := cfg.Inbounds[0].ListenPort
	filename := filepath.Join("/tmp/vlesssub", fmt.Sprintf("test-%d.json", port))

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}

	return filename, nil
}

// normalizeFingerprint maps common fingerprint values to sing-box format.
func normalizeFingerprint(fp string) string {
	if fp == "" {
		return "chrome"
	}
	fp = strings.ToLower(fp)
	switch fp {
	case "chrome":
		return "chrome"
	case "firefox":
		return "firefox"
	case "safari":
		return "safari"
	case "ios":
		return "ios"
	case "edge":
		return "edge"
	case "random", "randomized":
		return "randomized"
	default:
		return fp
	}
}
