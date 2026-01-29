package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"tunnelr/internal/tunnel"

	"github.com/gorilla/websocket"
)

func main() {
	// Parse command line arguments
	// Usage: tunnelr connect <port>
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "connect":
		if len(os.Args) < 3 {
			fmt.Println("Error: port number required")
			fmt.Println("Usage: tunnelr connect <port>")
			os.Exit(1)
		}
		port, err := strconv.Atoi(os.Args[2])
		if err != nil {
			fmt.Printf("Error: invalid port number: %s\n", os.Args[2])
			os.Exit(1)
		}
		runConnect(port)

	case "help", "--help", "-h":
		printUsage()

	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Tunnelr - Localhost to Live")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  tunnelr connect <port>   Create a tunnel to localhost:<port>")
	fmt.Println("  tunnelr help             Show this help message")
	fmt.Println("")
	fmt.Println("Example:")
	fmt.Println("  tunnelr connect 3000     Expose localhost:3000 to the internet")
}

func runConnect(localPort int) {
	// Server URL - in production, this would be configurable
	serverURL := getEnv("TUNNELR_SERVER", "ws://localhost:8080/ws")

	fmt.Printf("Connecting to tunnel server...\n")

	// Connect to server
	conn, _, err := websocket.DefaultDialer.Dial(serverURL, nil)
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()

	// Send register message
	regPayload := tunnel.TunnelRegister{LocalPort: localPort}
	regBytes, _ := json.Marshal(regPayload)
	regMsg := tunnel.Message{
		Type:    tunnel.TypeTunnelRegister,
		Payload: regBytes,
	}
	regMsgBytes, _ := json.Marshal(regMsg)

	if err := conn.WriteMessage(websocket.TextMessage, regMsgBytes); err != nil {
		log.Fatalf("Failed to register tunnel: %v", err)
	}

	// Wait for tunnel assignment
	_, assignBytes, err := conn.ReadMessage()
	if err != nil {
		log.Fatalf("Failed to receive tunnel assignment: %v", err)
	}

	var assignMsg tunnel.Message
	if err := json.Unmarshal(assignBytes, &assignMsg); err != nil {
		log.Fatalf("Invalid assignment message: %v", err)
	}

	var assigned tunnel.TunnelAssigned
	if err := json.Unmarshal(assignMsg.Payload, &assigned); err != nil {
		log.Fatalf("Invalid assignment payload: %v", err)
	}

	// Show the user their tunnel URL
	fmt.Println("")
	fmt.Println("Tunnel established!")
	fmt.Println("")
	fmt.Printf("  Public URL:  %s\n", assigned.PublicURL)
	fmt.Printf("  Forwarding:  %s -> http://localhost:%d\n", assigned.PublicURL, localPort)
	fmt.Println("")
	fmt.Println("Press Ctrl+C to close the tunnel")
	fmt.Println("")

	// Handle Ctrl+C
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	// Channel to signal when we should exit
	done := make(chan struct{})

	// Listen for incoming requests
	go func() {
		defer close(done)
		handleIncomingRequests(conn, localPort)
	}()

	// Wait for interrupt or connection close
	select {
	case <-interrupt:
		fmt.Println("\nClosing tunnel...")
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	case <-done:
		fmt.Println("Connection closed by server")
	}
}

// handleIncomingRequests listens for HTTP requests from the server
func handleIncomingRequests(conn *websocket.Conn, localPort int) {
	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("Connection error: %v", err)
			}
			return
		}

		var msg tunnel.Message
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Printf("Invalid message: %v", err)
			continue
		}

		if msg.Type == tunnel.TypeHTTPRequest {
			var req tunnel.HTTPRequest
			if err := json.Unmarshal(msg.Payload, &req); err != nil {
				log.Printf("Invalid request: %v", err)
				continue
			}

			// Process request in a goroutine so we can handle concurrent requests
			go processRequest(conn, localPort, &req)
		}
	}
}

// processRequest forwards an HTTP request to localhost and sends the response back
func processRequest(conn *websocket.Conn, localPort int, req *tunnel.HTTPRequest) {
	fmt.Printf("%s %s\n", req.Method, req.Path)

	// Build the local URL
	localURL := fmt.Sprintf("http://localhost:%d%s", localPort, req.Path)

	// Create the HTTP request
	httpReq, err := http.NewRequest(req.Method, localURL, bytes.NewReader(req.Body))
	if err != nil {
		sendErrorResponse(conn, req.ID, 500, "Failed to create request")
		return
	}

	// Copy headers
	for key, value := range req.Headers {
		// Skip hop-by-hop headers
		if key == "Connection" || key == "Keep-Alive" || key == "Transfer-Encoding" {
			continue
		}
		httpReq.Header.Set(key, value)
	}

	// Make the request to localhost
	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Printf("  -> Error: %v\n", err)
		sendErrorResponse(conn, req.ID, 502, "Failed to reach localhost")
		return
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		sendErrorResponse(conn, req.ID, 500, "Failed to read response")
		return
	}

	// Convert response headers
	headers := make(map[string]string)
	for key, values := range resp.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	fmt.Printf("  -> %d %s (%d bytes)\n", resp.StatusCode, resp.Status, len(body))

	// Send response back through WebSocket
	httpResp := tunnel.HTTPResponse{
		ID:         req.ID,
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       body,
	}

	respBytes, _ := json.Marshal(httpResp)
	msg := tunnel.Message{
		Type:    tunnel.TypeHTTPResponse,
		Payload: respBytes,
	}
	msgBytes, _ := json.Marshal(msg)

	if err := conn.WriteMessage(websocket.TextMessage, msgBytes); err != nil {
		log.Printf("Failed to send response: %v", err)
	}
}

// sendErrorResponse sends an error response back through the tunnel
func sendErrorResponse(conn *websocket.Conn, reqID string, statusCode int, message string) {
	resp := tunnel.HTTPResponse{
		ID:         reqID,
		StatusCode: statusCode,
		Headers:    map[string]string{"Content-Type": "text/plain"},
		Body:       []byte(message),
	}

	respBytes, _ := json.Marshal(resp)
	msg := tunnel.Message{
		Type:    tunnel.TypeHTTPResponse,
		Payload: respBytes,
	}
	msgBytes, _ := json.Marshal(msg)

	conn.WriteMessage(websocket.TextMessage, msgBytes)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
