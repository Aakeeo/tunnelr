package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"tunnelr/internal/tunnel"

	"github.com/gorilla/websocket"
)

// Global registry of active tunnels
var registry = tunnel.NewRegistry()

// pendingRequests tracks HTTP requests waiting for responses
// Maps request ID -> channel that will receive the response
var pendingRequests = struct {
	sync.RWMutex
	m map[string]chan *tunnel.HTTPResponse
}{m: make(map[string]chan *tunnel.HTTPResponse)}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Config - in production, these come from environment variables
var (
	baseDomain  = getEnv("BASE_DOMAIN", "localhost")  // e.g., "tunnelr.io"
	serverPort  = getEnv("PORT", "8080")
	routingMode = getEnv("ROUTING_MODE", "subdomain") // "subdomain" or "path"
)

func main() {
	// Route for CLI to establish tunnel
	http.HandleFunc("/ws", handleTunnelConnection)

	// Health check
	http.HandleFunc("/health", handleHealth)

	// Domain status check - shows if domain is properly configured
	http.HandleFunc("/status", handleStatus)

	// All other requests - check if it's a tunnel subdomain
	http.HandleFunc("/", handleRequest)

	addr := ":" + serverPort
	fmt.Printf("Tunnel server starting on %s\n", addr)
	fmt.Printf("Base domain: %s\n", baseDomain)
	fmt.Printf("Routing mode: %s\n", routingMode)

	if routingMode == "path" {
		fmt.Printf("Tunnel URLs will be: https://%s/t/<tunnel-id>/...\n", baseDomain)
	} else {
		fmt.Printf("Tunnel URLs will be: https://<tunnel-id>.%s/...\n", baseDomain)
	}

	log.Fatal(http.ListenAndServe(addr, nil))
}

// handleTunnelConnection handles WebSocket connections from CLI clients
func handleTunnelConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	log.Printf("New CLI client connected from %s", r.RemoteAddr)

	// Wait for the CLI to send a register message
	_, msgBytes, err := conn.ReadMessage()
	if err != nil {
		log.Printf("Failed to read register message: %v", err)
		conn.Close()
		return
	}

	var msg tunnel.Message
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		log.Printf("Invalid message format: %v", err)
		conn.Close()
		return
	}

	if msg.Type != tunnel.TypeTunnelRegister {
		log.Printf("Expected register message, got: %s", msg.Type)
		conn.Close()
		return
	}

	var reg tunnel.TunnelRegister
	if err := json.Unmarshal(msg.Payload, &reg); err != nil {
		log.Printf("Invalid register payload: %v", err)
		conn.Close()
		return
	}

	// Register the tunnel
	tunnelID := registry.Register(conn, reg.LocalPort)
	log.Printf("Tunnel registered: %s -> localhost:%d", tunnelID, reg.LocalPort)

	// Send back the assigned tunnel info
	// URL format depends on routing mode
	var publicURL string
	if routingMode == "path" {
		publicURL = fmt.Sprintf("https://%s/t/%s", baseDomain, tunnelID)
	} else {
		publicURL = fmt.Sprintf("https://%s.%s", tunnelID, baseDomain)
	}

	assigned := tunnel.TunnelAssigned{
		TunnelID:  tunnelID,
		PublicURL: publicURL,
	}

	assignedBytes, _ := json.Marshal(assigned)
	response := tunnel.Message{
		Type:    tunnel.TypeTunnelAssigned,
		Payload: assignedBytes,
	}

	responseBytes, _ := json.Marshal(response)
	if err := conn.WriteMessage(websocket.TextMessage, responseBytes); err != nil {
		log.Printf("Failed to send tunnel assignment: %v", err)
		registry.Remove(tunnelID)
		conn.Close()
		return
	}

	// Listen for responses from CLI (runs until connection closes)
	handleCLIResponses(conn, tunnelID)
}

// handleCLIResponses reads responses from CLI and routes them to waiting HTTP requests
func handleCLIResponses(conn *websocket.Conn, tunnelID string) {
	defer func() {
		registry.Remove(tunnelID)
		conn.Close()
		log.Printf("Tunnel disconnected: %s", tunnelID)
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			return
		}

		var msg tunnel.Message
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Printf("Invalid message: %v", err)
			continue
		}

		if msg.Type == tunnel.TypeHTTPResponse {
			var resp tunnel.HTTPResponse
			if err := json.Unmarshal(msg.Payload, &resp); err != nil {
				log.Printf("Invalid response payload: %v", err)
				continue
			}

			// Find the waiting request and send the response
			pendingRequests.RLock()
			ch, exists := pendingRequests.m[resp.ID]
			pendingRequests.RUnlock()

			if exists {
				ch <- &resp
			}
		}
	}
}

// handleRequest handles incoming HTTP requests and routes to tunnels
func handleRequest(w http.ResponseWriter, r *http.Request) {
	var tunnelID string
	var forwardPath string

	if routingMode == "path" {
		// Path-based routing: /t/<tunnel-id>/...
		tunnelID, forwardPath = extractFromPath(r.URL.Path)
	} else {
		// Subdomain-based routing: <tunnel-id>.domain.com
		tunnelID = extractSubdomain(r.Host)
		forwardPath = r.URL.RequestURI()
	}

	// If no tunnel ID, show landing page or 404
	if tunnelID == "" {
		if r.URL.Path == "/" {
			showLandingPage(w)
			return
		}
		http.NotFound(w, r)
		return
	}

	// Find the tunnel
	tun, exists := registry.Get(tunnelID)
	if !exists {
		http.Error(w, "Tunnel not found: "+tunnelID, http.StatusNotFound)
		return
	}

	// Forward the request through the tunnel
	forwardRequest(w, r, tun, forwardPath)
}

// showLandingPage displays the server info
func showLandingPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "Tunnelr - Localhost to Live")
	fmt.Fprintln(w, "")
	fmt.Fprintf(w, "Routing mode: %s\n", routingMode)
	fmt.Fprintf(w, "Active tunnels: %d\n", registry.Count())
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage: tunnelr connect <port>")
	if routingMode == "path" {
		fmt.Fprintf(w, "URLs:  https://%s/t/<tunnel-id>/your-path\n", baseDomain)
	} else {
		fmt.Fprintf(w, "URLs:  https://<tunnel-id>.%s/your-path\n", baseDomain)
	}
}

// extractFromPath extracts tunnel ID from path-based routing
// e.g., "/t/abc123/webhook" -> "abc123", "/webhook"
// e.g., "/t/abc123" -> "abc123", "/"
func extractFromPath(path string) (tunnelID string, forwardPath string) {
	// Must start with /t/
	if !strings.HasPrefix(path, "/t/") {
		return "", ""
	}

	// Remove /t/ prefix
	remaining := strings.TrimPrefix(path, "/t/")

	// Split by next slash
	parts := strings.SplitN(remaining, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return "", ""
	}

	tunnelID = parts[0]
	if len(parts) > 1 {
		forwardPath = "/" + parts[1]
	} else {
		forwardPath = "/"
	}

	return tunnelID, forwardPath
}

// forwardRequest sends an HTTP request through the WebSocket tunnel
func forwardRequest(w http.ResponseWriter, r *http.Request, tun *tunnel.Tunnel, forwardPath string) {
	// Generate unique request ID
	requestID := fmt.Sprintf("%d", time.Now().UnixNano())

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}

	// Convert headers to simple map
	headers := make(map[string]string)
	for key, values := range r.Header {
		headers[key] = strings.Join(values, ", ")
	}

	// Build the request message
	httpReq := tunnel.HTTPRequest{
		ID:      requestID,
		Method:  r.Method,
		Path:    forwardPath, // Use the processed path (stripped of /t/<id> if path-based)
		Headers: headers,
		Body:    body,
	}

	reqBytes, _ := json.Marshal(httpReq)
	msg := tunnel.Message{
		Type:    tunnel.TypeHTTPRequest,
		Payload: reqBytes,
	}
	msgBytes, _ := json.Marshal(msg)

	// Create a channel to receive the response
	respChan := make(chan *tunnel.HTTPResponse, 1)

	pendingRequests.Lock()
	pendingRequests.m[requestID] = respChan
	pendingRequests.Unlock()

	// Clean up when done
	defer func() {
		pendingRequests.Lock()
		delete(pendingRequests.m, requestID)
		pendingRequests.Unlock()
	}()

	// Send request to CLI
	if err := tun.Conn.WriteMessage(websocket.TextMessage, msgBytes); err != nil {
		http.Error(w, "Failed to forward request", http.StatusBadGateway)
		return
	}

	// Wait for response with timeout
	select {
	case resp := <-respChan:
		// Write response headers
		for key, value := range resp.Headers {
			w.Header().Set(key, value)
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(resp.Body)

	case <-time.After(30 * time.Second):
		http.Error(w, "Tunnel timeout", http.StatusGatewayTimeout)
	}
}

// extractSubdomain gets the subdomain from a host
// e.g., "abc123.tunnelr.io" -> "abc123"
// e.g., "tunnelr.io" -> ""
// e.g., "abc123.localhost:8080" -> "abc123"
func extractSubdomain(host string) string {
	// Remove port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	// Split by dots
	parts := strings.Split(host, ".")

	// Need at least subdomain.domain.tld (3 parts) or subdomain.localhost (2 parts)
	if len(parts) < 2 {
		return ""
	}

	// Check if it's just the base domain
	if host == baseDomain {
		return ""
	}

	// For localhost: abc123.localhost -> abc123
	if parts[len(parts)-1] == "localhost" && len(parts) == 2 {
		return parts[0]
	}

	// For real domains: abc123.tunnelr.io -> abc123
	if len(parts) >= 3 {
		return parts[0]
	}

	return ""
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "ok\nactive_tunnels: %d\n", registry.Count())
}

// handleStatus checks if the domain is properly configured
func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := DomainStatus{
		BaseDomain:    baseDomain,
		ServerPort:    serverPort,
		RoutingMode:   routingMode,
		ActiveTunnels: registry.Count(),
	}

	// Check if base domain resolves
	status.DomainCheck = checkDomain(baseDomain)

	// For subdomain mode, also check wildcard
	if routingMode == "subdomain" {
		testSubdomain := "test-dns-check." + baseDomain
		status.WildcardCheck = checkDomain(testSubdomain)
		status.Ready = status.DomainCheck.OK && status.WildcardCheck.OK
	} else {
		// Path mode doesn't need wildcard DNS
		status.WildcardCheck = DNSCheck{Domain: "N/A (path mode)", OK: true}
		status.Ready = status.DomainCheck.OK
	}

	// Provide helpful message
	if !status.DomainCheck.OK {
		status.Message = fmt.Sprintf("Domain %s does not resolve. Add an A record pointing to your server's IP.", baseDomain)
	} else if routingMode == "subdomain" && !status.WildcardCheck.OK {
		status.Message = fmt.Sprintf("Wildcard subdomain not configured. Add an A record for *.%s pointing to your server's IP.", baseDomain)
	} else {
		if routingMode == "path" {
			status.Message = fmt.Sprintf("Ready! Tunnel URLs: https://%s/t/<tunnel-id>/...", baseDomain)
		} else {
			status.Message = fmt.Sprintf("Ready! Tunnel URLs: https://<tunnel-id>.%s/...", baseDomain)
		}
	}

	json.NewEncoder(w).Encode(status)
}

// DomainStatus represents the configuration status
type DomainStatus struct {
	Ready         bool     `json:"ready"`
	Message       string   `json:"message"`
	BaseDomain    string   `json:"base_domain"`
	RoutingMode   string   `json:"routing_mode"`
	ServerPort    string   `json:"server_port"`
	ActiveTunnels int      `json:"active_tunnels"`
	DomainCheck   DNSCheck `json:"domain_check"`
	WildcardCheck DNSCheck `json:"wildcard_check"`
}

// DNSCheck represents a DNS lookup result
type DNSCheck struct {
	Domain string   `json:"domain"`
	OK     bool     `json:"ok"`
	IPs    []string `json:"ips,omitempty"`
	Error  string   `json:"error,omitempty"`
}

// checkDomain performs a DNS lookup
func checkDomain(domain string) DNSCheck {
	check := DNSCheck{Domain: domain}

	ips, err := net.LookupIP(domain)
	if err != nil {
		check.OK = false
		check.Error = err.Error()
		return check
	}

	check.OK = len(ips) > 0
	for _, ip := range ips {
		check.IPs = append(check.IPs, ip.String())
	}

	return check
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
