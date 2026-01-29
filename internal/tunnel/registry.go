package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/gorilla/websocket"
)

// Tunnel represents an active tunnel connection
type Tunnel struct {
	ID        string          // Unique identifier (subdomain)
	Conn      *websocket.Conn // WebSocket connection to CLI
	LocalPort int             // Port on the CLI's machine
}

// Registry keeps track of all active tunnels
// Multiple goroutines will access this, so we need a mutex (lock)
type Registry struct {
	// mu = mutex, protects the tunnels map from concurrent access
	// In Go, you embed sync.Mutex directly in the struct
	mu      sync.RWMutex
	tunnels map[string]*Tunnel
}

// NewRegistry creates an empty registry
// In Go, functions starting with "New" are constructors by convention
func NewRegistry() *Registry {
	return &Registry{
		tunnels: make(map[string]*Tunnel),
	}
}

// Register adds a new tunnel and returns its ID
func (r *Registry) Register(conn *websocket.Conn, localPort int) string {
	// Generate a random ID for the subdomain
	id := generateID()

	// Lock for writing (exclusive access)
	r.mu.Lock()
	// defer unlocks when function exits - prevents forgetting to unlock
	defer r.mu.Unlock()

	r.tunnels[id] = &Tunnel{
		ID:        id,
		Conn:      conn,
		LocalPort: localPort,
	}

	return id
}

// Get retrieves a tunnel by ID
// Returns (tunnel, true) if found, (nil, false) if not
func (r *Registry) Get(id string) (*Tunnel, bool) {
	// RLock = read lock (multiple readers OK, blocks writers)
	r.mu.RLock()
	defer r.mu.RUnlock()

	tunnel, exists := r.tunnels[id]
	return tunnel, exists
}

// Remove deletes a tunnel (called when CLI disconnects)
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.tunnels, id)
}

// Count returns how many active tunnels exist
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.tunnels)
}

// generateID creates a random 6-character hex string
// e.g., "a1b2c3" - short enough to type, random enough to not collide
func generateID() string {
	bytes := make([]byte, 3) // 3 bytes = 6 hex characters
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
