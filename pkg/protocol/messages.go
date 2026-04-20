package protocol

import "encoding/json"

// Frame type constants — 1 byte header in every frame.
const (
	TypeAuth       byte = 0x01 // client → server: send token
	TypeAuthOK     byte = 0x02 // server → client: auth succeeded, assigned IP
	TypeAuthFail   byte = 0x03 // server → client: auth failed
	TypeIPPacket   byte = 0x04 // bidirectional: raw IP packet
	TypePing       byte = 0x05 // keepalive ping
	TypePong       byte = 0x06 // keepalive pong
	TypeDisconnect byte = 0x07 // graceful disconnect

	// Proxy mode — TCP port-forward without TUN device (no CAP_NET_ADMIN needed).
	TypeProxyDial  byte = 0x08 // client → server: open a TCP stream to host:port
	TypeProxyOK    byte = 0x09 // server → client: stream opened
	TypeProxyFail  byte = 0x0A // server → client: stream open failed
	TypeProxyData  byte = 0x0B // bidirectional: data for a stream; payload = [4 bytes stream_id][data]
	TypeProxyClose byte = 0x0C // bidirectional: close a stream; payload = [4 bytes stream_id]
)

// Wire format (big-endian):
//   [4 bytes: content_len][1 byte: type][content_len-1 bytes: payload]
// content_len includes the type byte, so payload = content_len - 1 bytes.

// AuthRequest is the JSON payload for TypeAuth.
type AuthRequest struct {
	Token string `json:"token"`
	Mode  string `json:"mode,omitempty"` // "" or "tun" = TUN mode; "proxy" = proxy port-forward mode
}

// ProxyDialRequest is the JSON payload for TypeProxyDial.
type ProxyDialRequest struct {
	StreamID uint32 `json:"stream_id"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
}

// ProxyDialOK is the JSON payload for TypeProxyOK.
type ProxyDialOK struct {
	StreamID uint32 `json:"stream_id"`
}

// ProxyDialFail is the JSON payload for TypeProxyFail.
type ProxyDialFail struct {
	StreamID uint32 `json:"stream_id"`
	Reason   string `json:"reason"`
}

// AuthOKResponse is the JSON payload for TypeAuthOK.
type AuthOKResponse struct {
	ClientIP string `json:"client_ip"` // e.g. "10.0.0.2"
	ServerIP string `json:"server_ip"` // e.g. "10.0.0.1"
	Subnet   string `json:"subnet"`    // e.g. "10.0.0.0/24"
}

// AuthFailResponse is the JSON payload for TypeAuthFail.
type AuthFailResponse struct {
	Reason string `json:"reason"`
}

func Encode(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func Decode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
