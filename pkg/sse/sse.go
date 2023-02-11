package sse

import (
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

type Event struct {
	// Events are pushed to this channel by the main events-gathering routine
	Message chan string

	// New client connections
	NewClients chan chan string

	// Closed client connections
	ClosedClients chan chan string

	// Total client connections
	TotalClients map[chan string]bool

	OnNewClientCallback func() string
}

// New event messages are broadcast to all registered client connection channels
type ClientChan chan string

// It Listens all incoming requests from clients.
// Handles addition and removal of clients and broadcast messages to clients.
func (stream *Event) listen() {
	for {
		select {
		// Add new available client
		case client := <-stream.NewClients:
			stream.TotalClients[client] = true
			log.Info().
				Int("clientCount", len(stream.TotalClients)).
				Msg("client added")
			if stream.OnNewClientCallback != nil {
				client <- stream.OnNewClientCallback()
			}

		// Remove closed client
		case client := <-stream.ClosedClients:
			delete(stream.TotalClients, client)
			close(client)
			log.Info().
				Int("clientCount", len(stream.TotalClients)).
				Msg("client removed")

		// Broadcast message to client
		case eventMsg := <-stream.Message:
			for clientMessageChan := range stream.TotalClients {
				clientMessageChan <- eventMsg
			}
			log.Trace().
				Str("message", eventMsg).
				Int("clientCount", len(stream.TotalClients)).
				Msg("sent server event")
		}
	}
}

func (stream *Event) ServeHTTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Initialize client channel
		clientChan := make(ClientChan)

		// Send new connection to event server
		stream.NewClients <- clientChan

		defer func() {
			// Send closed connection to event server
			stream.ClosedClients <- clientChan
		}()

		c.Set("clientChan", clientChan)

		c.Next()
	}
}

// Initialize event and Start processing requests
func NewServer(onNewClientCallback func() string) (event *Event) {
	event = &Event{
		Message:             make(chan string),
		NewClients:          make(chan chan string),
		ClosedClients:       make(chan chan string),
		TotalClients:        make(map[chan string]bool),
		OnNewClientCallback: onNewClientCallback,
	}

	go event.listen()

	return
}

func HeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("Transfer-Encoding", "chunked")
		c.Next()
	}
}
