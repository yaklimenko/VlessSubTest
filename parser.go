package main

import (
	"fmt"
	"net/url"
	"strings"
)

// VlessKey holds parsed vless:// URI fields.
type VlessKey struct {
	Raw     string // original URI
	Remark  string // from # fragment
	UUID    string
	Address string
	Port    string

	// Query params
	Security       string // "reality", "tls", "none"
	Type           string // "tcp", "grpc", "ws", "httpupgrade", etc.
	Flow           string // "xtls-rprx-vision"
	PublicKey      string // pbk
	ShortID        string // sid
	SpiderX        string // spx
	Fingerprint    string // fp
	ServerName     string // sni
	Path           string // ws path, grpc serviceName
	Host           string // ws/grpc host
	Encryption     string // "none"
	PacketEncoding string // "xudp", "packetaddr"
	AllowInsecure  string // "0" or "1"
}

// ParseVlessURI parses a vless:// URI string.
func ParseVlessURI(raw string) (*VlessKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty URI")
	}

	// Remove vless:// prefix
	clean := raw
	if strings.HasPrefix(strings.ToLower(clean), "vless://") {
		clean = clean[len("vless://"):]
	} else {
		return nil, fmt.Errorf("not a vless:// URI")
	}

	// Extract # fragment (remark)
	var remark string
	if idx := strings.Index(clean, "#"); idx != -1 {
		remark = clean[idx+1:]
		// URL-decode the remark
		if unescaped, err := url.QueryUnescape(remark); err == nil {
			remark = unescaped
		}
		clean = clean[:idx]
	}

	// Split at first '@' to get userinfo and host:port?query
	atIdx := strings.Index(clean, "@")
	if atIdx == -1 {
		return nil, fmt.Errorf("missing @ in URI")
	}
	userinfo := clean[:atIdx]
	hostPart := clean[atIdx+1:]

	// Parse userinfo: uuid
	uuid := userinfo

	// Split hostPart into host:port and query
	var hostPort, queryStr string
	if qIdx := strings.Index(hostPart, "?"); qIdx != -1 {
		hostPort = hostPart[:qIdx]
		queryStr = hostPart[qIdx+1:]
	} else {
		hostPort = hostPart
	}

	// Split host:port
	var address, port string
	if colonIdx := strings.LastIndex(hostPort, ":"); colonIdx != -1 {
		address = hostPort[:colonIdx]
		if bracketIdx := strings.Index(address, "["); bracketIdx != -1 {
			// IPv6
			closeBracket := strings.Index(address, "]")
			if closeBracket == -1 {
				return nil, fmt.Errorf("invalid IPv6 address")
			}
			address = address[bracketIdx+1 : closeBracket]
		}
		port = hostPort[colonIdx+1:]
	} else {
		address = hostPort
		port = "443"
	}

	// Parse query params
	query, err := url.ParseQuery(queryStr)
	if err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}

	k := &VlessKey{
		Raw:            raw,
		Remark:         remark,
		UUID:           uuid,
		Address:        address,
		Port:           port,
		Security:       getQuery(query, "security", "none"),
		Type:           getQuery(query, "type", "tcp"),
		Flow:           getQuery(query, "flow", ""),
		PublicKey:      getQuery(query, "pbk", getQuery(query, "publicKey", "")),
		ShortID:        getQuery(query, "sid", getQuery(query, "shortId", "")),
		SpiderX:        getQuery(query, "spx", getQuery(query, "spiderX", "")),
		Fingerprint:    getQuery(query, "fp", getQuery(query, "fingerprint", "")),
		ServerName:     getQuery(query, "sni", getQuery(query, "serverName", "")),
		Path:           getQuery(query, "path", ""),
		Host:           getQuery(query, "host", ""),
		Encryption:     getQuery(query, "encryption", "none"),
		PacketEncoding: getQuery(query, "packet_encoding", ""),
		AllowInsecure:  getQuery(query, "allowInsecure", ""),
	}

	return k, nil
}

func getQuery(q url.Values, key, defaultVal string) string {
	if vals, ok := q[key]; ok && len(vals) > 0 {
		return vals[0]
	}
	return defaultVal
}
