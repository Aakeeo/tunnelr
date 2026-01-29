package tunnel

// This file defines the "language" that server and CLI speak over WebSocket
// We serialize HTTP requests/responses to JSON and send them through the tunnel

// MessageType identifies what kind of message this is
type MessageType string

const (
	// Server -> CLI: "here's an HTTP request, please handle it"
	TypeHTTPRequest MessageType = "http_request"

	// CLI -> Server: "here's the response from localhost"
	TypeHTTPResponse MessageType = "http_response"

	// Server -> CLI: "here's your assigned tunnel ID"
	TypeTunnelAssigned MessageType = "tunnel_assigned"

	// CLI -> Server: "I want to register a tunnel for this port"
	TypeTunnelRegister MessageType = "tunnel_register"
)

// Message is the envelope for all WebSocket communication
// In Go, struct fields with `json:"..."` tags define how they serialize to JSON
type Message struct {
	Type    MessageType `json:"type"`
	Payload []byte      `json:"payload"` // The actual data (varies by type)
}

// TunnelAssigned is sent from server to CLI after connection
type TunnelAssigned struct {
	TunnelID  string `json:"tunnel_id"`  // e.g., "abc123"
	PublicURL string `json:"public_url"` // e.g., "https://abc123.tunnelr.io"
}

// TunnelRegister is sent from CLI to server when connecting
type TunnelRegister struct {
	LocalPort int `json:"local_port"` // e.g., 3000
}

// HTTPRequest represents an incoming HTTP request to forward
type HTTPRequest struct {
	ID      string            `json:"id"`      // Unique ID to match response
	Method  string            `json:"method"`  // GET, POST, etc.
	Path    string            `json:"path"`    // /api/webhook
	Headers map[string]string `json:"headers"` // HTTP headers
	Body    []byte            `json:"body"`    // Request body
}

// HTTPResponse is what the CLI sends back after hitting localhost
type HTTPResponse struct {
	ID         string            `json:"id"`          // Matches the request ID
	StatusCode int               `json:"status_code"` // 200, 404, etc.
	Headers    map[string]string `json:"headers"`     // Response headers
	Body       []byte            `json:"body"`        // Response body
}
