package main

import (
	"coffee-ctl/pkg/config"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"syscall"
	"time"

	"os/signal"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gin-gonic/gin"
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
			client <- stream.OnNewClientCallback()

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
		}
	}
}

func (stream *Event) serveHTTP() gin.HandlerFunc {
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

const defaultTimeToSwitchOff = 5 * time.Minute // time.Hour

func NewCoffeeCtl() (c CoffeeCtl) {
	r := gin.Default()
	s := NewServer(c.GetStatusString)
	c = CoffeeCtl{timeToSwitchOff: defaultTimeToSwitchOff, router: r, stream: s}
	return c
}

type CoffeeCtl struct {
	timeToSwitchOff  time.Duration
	lastRunTime      time.Time
	countdownRunning bool
	switchState      bool
	router           *gin.Engine
	stream           *Event
	lastStatus       Status
}

func (c *CoffeeCtl) makeRoutes() {
	// Authorized client can stream the event
	// Add event-streaming headers
	c.router.GET("/timer/stream", HeadersMiddleware(), c.stream.serveHTTP(), func(c *gin.Context) {
		v, ok := c.Get("clientChan")
		if !ok {
			return
		}
		clientChan, ok := v.(ClientChan)
		if !ok {
			return
		}
		c.Stream(func(w io.Writer) bool {
			// Stream message to client from message channel
			if msg, ok := <-clientChan; ok {
				c.SSEvent("message", msg)
				return true
			}
			return false
		})
	})

	c.router.GET("/timer", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{
			"countdownNs": c.timeToSwitchOff,
			"switchOffAt": time.Now().Add(c.timeToSwitchOff),
		})
	})

	c.router.POST("/timer", func(ctx *gin.Context) {
		var timer struct {
			Delta time.Duration `json:"delta"`
		}
		err := ctx.ShouldBindJSON(&timer)
		if err != nil {
			// return 400?
		}

		c.AddTime(timer.Delta)

		ctx.JSON(http.StatusOK, c.GetStatus())
	})

	c.router.POST("/on", func(ctx *gin.Context) {
		c.SwitchOn()
		ctx.Status(200)
	})

	c.router.POST("/off", func(ctx *gin.Context) {
		c.SwitchOff()
		ctx.Status(200)
	})
}

type Status struct {
	CountdownNs      time.Duration `json:"countdownNs"`
	SwitchOffAt      *time.Time    `json:"switchOffAt"`
	CountdownRunning bool          `json:"countdownRunning"`
	SwitchState      bool          `json:"switchState"`
}

func (c *CoffeeCtl) GetStatus() Status {
	var switchOffAtPtr *time.Time = nil
	if c.countdownRunning {
		switchOffAt := time.Now().Add(c.timeToSwitchOff)
		switchOffAtPtr = &switchOffAt
	}
	return Status{
		CountdownNs:      c.timeToSwitchOff,
		SwitchOffAt:      switchOffAtPtr,
		CountdownRunning: c.countdownRunning,
		SwitchState:      c.switchState,
	}
}

func (c *CoffeeCtl) GetStatusString() string {
	payload, _ := json.Marshal(c.GetStatus())
	// TODO: error handling?
	return string(payload)
}

func (c *CoffeeCtl) startCountdown() {
	log.Debug().Msg("starting countdown")
	c.countdownRunning = true

}

func (c *CoffeeCtl) stopCountdown() {
	log.Debug().Msg("stopping countdown")
	c.countdownRunning = false
	c.timeToSwitchOff = defaultTimeToSwitchOff

}

func (c *CoffeeCtl) SwitchOn() {
	log.Debug().Msg("switching on")
	// this should reflect the actual plug switch state
	c.switchState = true
	c.startCountdown()
}

func (c *CoffeeCtl) SwitchOff() {
	log.Debug().Msg("switching off")
	c.switchState = false
	c.stopCountdown()
}

func (c *CoffeeCtl) AddTime(d time.Duration) {
	c.timeToSwitchOff += d
	if c.timeToSwitchOff < 0 {
		c.timeToSwitchOff = 0
	}

	log.Debug().
		Int("added duration", int(d)).
		Int("new timeToSwitchOff", int(c.timeToSwitchOff)).
		Msg("adding time to timeToSwitchOff")
}

func (c *CoffeeCtl) emitStatus() {
	status := c.GetStatus()
	if c.lastStatus != status {
		log.Debug().
			Interface("status", status).
			Msg("status changed")
		payload, _ := json.Marshal(status)
		// TODO: error handling?
		c.stream.Message <- string(payload)
		c.lastStatus = status
	}
}

func (c *CoffeeCtl) Run() {
	c.makeRoutes()
	go c.router.Run()

	var deltaT time.Duration = 0
	for {
		if c.lastRunTime.IsZero() {
			c.lastRunTime = time.Now()
		} else {
			deltaT = time.Since(c.lastRunTime)
			c.lastRunTime = time.Now()
		}

		log.Trace().Int("deltaT", int(deltaT)).Msg("running coffee-ctl loop")

		if c.countdownRunning {
			c.timeToSwitchOff -= deltaT
		}

		if c.timeToSwitchOff <= 0 {
			c.SwitchOff()
		}

		c.emitStatus()

		time.Sleep(200 * time.Millisecond)
	}
}

func main() {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	log.Logger = log.Output(
		zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339Nano},
	)

	configFilePath := "config.yaml"
	conf := config.Config{}

	err := config.ReadConfig(configFilePath, &conf)
	if err != nil {
		log.Error().Err(err).Str("configFilePath", configFilePath).Msg("error reading config file")
		os.Exit(1)
	}
	log.Info().
		Interface("conf", conf).
		Str("configFilePath", configFilePath).
		Msg("read config file")

	c := NewCoffeeCtl()

	go c.Run()

	// do stuff here

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	signal.Notify(sig, syscall.SIGTERM)

	<-sig
	log.Info().Msg("Exiting")
}
