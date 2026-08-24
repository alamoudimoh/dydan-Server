package realtime

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
)

// Connection represents a WebSocket or SSE connection with metadata
type Connection struct {
	ID           string
	CircleID     int
	UserID       int
	User         *uModel.User
	Conn         *websocket.Conn // nil for SSE connections
	Send         chan *Event
	LastActivity time.Time
	mu           sync.RWMutex
	closed       bool
	logger       *zap.SugaredLogger
	// authRevalidate, when set, re-checks the authoritative authentication
	// state of this connection's original claims against the database. It is
	// invoked on the write-pump ping tick so a revoked session cannot keep an
	// authenticated WebSocket open longer than one ping interval.
	authRevalidate func() error
}

// realtimeAuthProbeTimeout bounds each authoritative auth revalidation probe
// (WebSocket and SSE alike) so a slow database can never stall the validating
// caller indefinitely. Both paths fail closed: any probe error, including a
// deadline overrun, tears the connection down.
const realtimeAuthProbeTimeout = 5 * time.Second

// wsPingInterval controls the WebSocket server-ping cadence. It is also the
// maximum interval at which auth revalidation runs, i.e. the worst-case bound
// between an authoritative revocation commit and the connection being torn
// down.
var wsPingInterval = 54 * time.Second

// SetAuthRevalidator attaches the periodic security-state check used by the
// write pump. Passing nil disables revalidation.
func (c *Connection) SetAuthRevalidator(fn func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authRevalidate = fn
}

// NewConnection creates a new WebSocket or SSE connection
func NewConnection(id string, circleID, userID int, user *uModel.User, conn *websocket.Conn, logger *zap.SugaredLogger) *Connection {
	return &Connection{
		ID:           id,
		CircleID:     circleID,
		UserID:       userID,
		User:         user,
		Conn:         conn,
		Send:         make(chan *Event, 256), // Buffered channel for events
		LastActivity: time.Now().UTC(),
		closed:       false,
		logger:       logger,
	}
}

// IsStale checks if the connection is stale based on the threshold
func (c *Connection) IsStale(threshold time.Duration) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Since(c.LastActivity) > threshold
}

// IsClosed returns true if the connection is closed
func (c *Connection) IsClosed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.closed
}

// Close closes the WebSocket connection
func (c *Connection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	c.closed = true
	close(c.Send)

	// Only close websocket connection if it exists (SSE connections have nil Conn)
	if c.Conn != nil {
		c.Conn.Close()
	}

	c.logger.Debugw("Connection closed",
		"connectionId", c.ID,
		"userId", c.UserID,
		"circleId", c.CircleID,
		"connectionType", c.getConnectionType())
}

// getConnectionType returns the connection type for logging
func (c *Connection) getConnectionType() string {
	if c.Conn == nil {
		return "SSE"
	}
	return "WebSocket"
}

// UpdateActivity updates the last activity timestamp
func (c *Connection) UpdateActivity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.LastActivity = time.Now().UTC()
}

// SendEvent sends an event to the connection
func (c *Connection) SendEvent(event *Event) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return false
	}

	select {
	case c.Send <- event:
		return true
	default:
		// Channel is full, log warning and mark connection for closure.
		// The actual channel closing and connection cleanup is handled by Connection.Close().
		c.logger.Warnw("Connection send buffer full. Marking connection for closure.",
			"connectionId", c.ID,
			"userId", c.UserID,
			"circleId", c.CircleID,
			"bufferSize", cap(c.Send),
			"eventTypeAttempted", event.Type)

		c.closed = true // Mark as closed. Connection.Close() will handle actual closing.
		// NOTE: Removed close(c.Send) from here to prevent double-close panics.
		// NOTE: Removed WebSocket specific closing from here; Connection.Close() handles it.
		return false // Indicate that the event was not sent
	}
}

// StartReadPump starts the read pump for handling incoming messages
func (c *Connection) StartReadPump(pool *ConnectionPool) {
	defer func() {
		pool.RemoveConnection(c)
		c.Close()
	}()

	// Set read deadline and pong handler for heartbeat
	c.Conn.SetReadDeadline(time.Now().UTC().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.UpdateActivity()
		c.Conn.SetReadDeadline(time.Now().UTC().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.Errorw("WebSocket read error", "error", err, "connectionId", c.ID)
			}
			break
		}

		c.UpdateActivity()

		// Handle incoming message (e.g., ping/pong, client events)
		c.handleIncomingMessage(message)
	}
}

// StartWritePump starts the write pump for sending outgoing messages
func (c *Connection) StartWritePump() {
	ticker := time.NewTicker(wsPingInterval) // Send ping every 54 seconds
	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case event, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().UTC().Add(10 * time.Second))
			if !ok {
				// Channel closed
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// Send the event as JSON
			eventJSON, err := event.ToJSON()
			if err != nil {
				c.logger.Errorw("Failed to marshal event", "error", err, "connectionId", c.ID)
				continue
			}

			if err := c.Conn.WriteMessage(websocket.TextMessage, eventJSON); err != nil {
				c.logger.Errorw("Failed to write message", "error", err, "connectionId", c.ID)
				return
			}

		case <-ticker.C:
			// Bounded auth revalidation: every ping tick re-checks the
			// authoritative session/credential state. A committed revocation
			// (session, family, global, or credential-version bump) therefore
			// terminates this connection within at most one ping interval.
			c.mu.RLock()
			revalidate := c.authRevalidate
			c.mu.RUnlock()
			if revalidate != nil {
				if err := revalidate(); err != nil {
					c.logger.Infow("WebSocket authentication state revoked, closing connection",
						"connectionId", c.ID,
						"userId", c.UserID,
						"error", err)
					c.Conn.SetWriteDeadline(time.Now().UTC().Add(10 * time.Second))
					closeMsg := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "authentication revoked")
					c.Conn.WriteMessage(websocket.CloseMessage, closeMsg)
					return
				}
			}
			c.Conn.SetWriteDeadline(time.Now().UTC().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.logger.Errorw("Failed to write ping", "error", err, "connectionId", c.ID)
				return
			}
		}
	}
}

// handleIncomingMessage processes incoming WebSocket messages
func (c *Connection) handleIncomingMessage(message []byte) {
	// For now, we mainly handle ping/pong for keepalive
	// In the future, we could handle client-side events here
	c.logger.Debugw("Received message from client",
		"connectionId", c.ID,
		"message", string(message))
}

// WebSocketUpgrader configures the WebSocket upgrader
var WebSocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// For now, allow all origins - in production, implement proper origin checking
		return true
	},
}
