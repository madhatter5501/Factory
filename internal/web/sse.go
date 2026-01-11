package web

import (
	"fmt"
	"net/http"
)

// handleSSE handles Server-Sent Events for real-time updates.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	// Set headers for SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create channel for this client
	messageChan := make(chan string, 10)

	// Register client
	s.sseMu.Lock()
	s.sseClients[messageChan] = true
	s.sseMu.Unlock()

	// Cleanup on disconnect
	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, messageChan)
		s.sseMu.Unlock()
		close(messageChan)
	}()

	// Get the flusher
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send initial connection message
	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
	flusher.Flush()

	s.logger.Debug("SSE client connected")

	// Stream events to client
	for {
		select {
		case <-r.Context().Done():
			s.logger.Debug("SSE client disconnected")
			return
		case msg, ok := <-messageChan:
			if !ok {
				return
			}
			// Send as htmx-compatible event
			fmt.Fprintf(w, "event: %s\ndata: {\"type\":\"%s\"}\n\n", msg, msg)
			flusher.Flush()
		}
	}
}
