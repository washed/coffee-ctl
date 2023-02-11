package controller

import (
	"coffee-ctl/pkg/config"
	"coffee-ctl/pkg/mqttopts"
	"coffee-ctl/pkg/sse"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/washed/shelly-go"

	"github.com/gin-gonic/gin"
)

const defaultCountdownNs = 2 * time.Hour

func NewCoffeeCtl(conf config.Config) (c CoffeeCtl) {
	r := gin.Default()
	s := sse.NewServer(nil)

	mqttOpts := mqttopts.GetMQTTOpts()
	b := shelly.NewShellyButton1(conf.ShellyButton1ID, mqttOpts)
	p := shelly.NewShellyPlugS(conf.ShellyPlugSID, mqttOpts)

	redisURL := os.Getenv("REDIS_URL")
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Error().Err(err).Str("redisURL", redisURL).Msg("error parsing REDIS_URL")
	}

	rdb := redis.NewClient(redisOptions)

	c = CoffeeCtl{
		router:      r,
		stream:      s,
		button:      b,
		plugS:       p,
		redis:       rdb,
		commandChan: make(chan func(status *Status), 100),
	}

	return c
}

type ButtonBatteryStatus struct {
	Soc   float32 `json:"soc"`
	Valid bool    `json:"valid"`
}

type SwitchOffAtStatus struct {
	Time  time.Time `json:"time"`
	Valid bool      `json:"valid"`
}

type Status struct {
	CountdownNs         time.Duration       `json:"countdownNs"`
	SwitchOffAtStatus   SwitchOffAtStatus   `json:"switchOffAtStatus"`
	CountdownRunning    bool                `json:"countdownRunning"`
	IntendedSwitchState bool                `json:"intendedSwitchState"`
	SwitchState         bool                `json:"switchState"`
	ButtonBatteryStatus ButtonBatteryStatus `json:"buttonBatteryStatus"`
}

type CoffeeCtl struct {
	router      *gin.Engine
	stream      *sse.Event
	redis       *redis.Client
	commandChan chan func(status *Status)
	button      shelly.ShellyButton1
	plugS       shelly.ShellyPlugS
}

func (c *CoffeeCtl) makeRoutes() {
	c.router.GET(
		"/timer/stream",
		sse.HeadersMiddleware(),
		c.stream.ServeHTTP(),
		func(c *gin.Context) {
			v, ok := c.Get("clientChan")
			if !ok {
				return
			}
			clientChan, ok := v.(sse.ClientChan)
			if !ok {
				return
			}
			c.Stream(func(w io.Writer) bool {
				if msg, ok := <-clientChan; ok {
					c.SSEvent("message", msg)
					return true
				}
				return false
			})
		},
	)

	c.router.POST("/timer", func(ctx *gin.Context) {
		var timer struct {
			Delta time.Duration `json:"delta"`
		}
		err := ctx.ShouldBindJSON(&timer)
		if err != nil {
			// return 400?
		}

		c.commandChan <- func(status *Status) {
			c.addTime(status, timer.Delta)
		}

		ctx.Status(http.StatusOK)
	})

	c.router.POST("/on", func(ctx *gin.Context) {
		c.commandChan <- c.switchOn
		ctx.Status(http.StatusOK)
	})

	c.router.POST("/off", func(ctx *gin.Context) {
		c.commandChan <- c.switchOff
		ctx.Status(http.StatusOK)
	})
}

func (c *CoffeeCtl) getStatus() (*Status, error) {
	ctx := context.Background()
	statusStr, err := c.redis.Get(ctx, "coffee-ctl:status").Result()
	if err == redis.Nil {
		log.Warn().Msg("no status found in redis")

		status := Status{}
		c.setStatus(&status)
		return &status, nil
	} else if err != nil {
		log.Panic().Err(err).Msg("error reading status from redis")
		return nil, err
	}

	var status Status
	statusBytes := []byte(statusStr)
	err = json.Unmarshal(statusBytes, &status)
	log.Trace().Str("statusStr", statusStr).Interface("status", status).Msg("status from redis")
	if err != nil {
		log.Error().
			Err(err).
			Str("statusStr", statusStr).
			Bytes("statusBytes", statusBytes).
			Msg("error unmarshalling status")
	}
	return &status, nil
}

func (c *CoffeeCtl) setStatus(status *Status) {
	ctx := context.Background()
	statusBytes, err := json.Marshal(status)
	if err != nil {
		log.Error().Err(err).Msg("error marshalling status")
	}
	err = c.redis.Set(ctx, "coffee-ctl:status", statusBytes, time.Hour).Err()
	if err != nil {
		log.Panic().Err(err).Msg("error writing status to redis")
	}
}

func (c *CoffeeCtl) switchOn(status *Status) {
	log.Debug().Msg("switching on, starting countdown")
	c.plugS.SwitchOn()
	status.IntendedSwitchState = true
	status.CountdownRunning = true
}

func (c *CoffeeCtl) switchOff(status *Status) {
	log.Debug().Msg("switching off, stopping countdown")
	c.plugS.SwitchOff()
	status.IntendedSwitchState = false
	status.CountdownRunning = false
	status.CountdownNs = defaultCountdownNs
}

func (c *CoffeeCtl) subscribeSwitchState() {
	/*
		We call SwitchOn/Off here to correctly track state if the plug is turned on
		via the physical button on the plug or the web UI
	*/
	c.plugS.SubscribeRelayState(func() {
		c.commandChan <- func(status *Status) {
			if !status.SwitchState {
				status.SwitchState = true
				c.switchOn(status)
			}
		}
	}, func() {
		c.commandChan <- func(status *Status) {
			if status.SwitchState {
				status.SwitchState = false
				c.switchOff(status)
			}
		}
	})
}

func (c *CoffeeCtl) subscribeButtonEvents() {
	c.button.SubscribeInputEvent(func() {
		c.commandChan <- func(status *Status) {
			if status.SwitchState {
				c.switchOff(status)
			} else {
				c.switchOn(status)
			}
		}
	}, nil, nil, nil)
}

func (c *CoffeeCtl) subscribeButtonBattery() {
	c.button.SubscribeBattery(func(buttonBatterySoC float32) {
		c.commandChan <- func(status *Status) {
			status.ButtonBatteryStatus.Soc = buttonBatterySoC
			status.ButtonBatteryStatus.Valid = true
		}
	})
}

func (c *CoffeeCtl) addTime(status *Status, d time.Duration) {
	status.CountdownNs += d
	if status.CountdownNs < 0 {
		status.CountdownNs = 0
	}

	log.Debug().
		Int("added duration", int(d)).
		Int("new CountdownNs", int(status.CountdownNs)).
		Msg("adding time to CountdownNs")
}

func (c *CoffeeCtl) emitStatus(status *Status) {
	log.Trace().
		Interface("status", status).
		Msg("emitting status")

	payload, _ := json.Marshal(status)
	// TODO: error handling?

	c.stream.Message <- string(payload)
}

func (c *CoffeeCtl) Run() {
	c.plugS.Connect()
	defer c.plugS.Close()
	c.subscribeSwitchState()

	c.button.Connect()
	defer c.button.Close()
	c.subscribeButtonEvents()
	c.subscribeButtonBattery()

	c.makeRoutes()

	go c.router.Run()

	tick := time.Tick(200 * time.Millisecond)

	var lastRunTime time.Time

	for {
		select {
		case command := <-c.commandChan:
			log.Debug().Msg("received controller command")
			status, _ := c.getStatus()
			// TODO: error handling
			command(status)
			c.setStatus(status)

		case now := <-tick:
			if lastRunTime.IsZero() {
				lastRunTime = now
			}
			deltaT := time.Since(lastRunTime)
			lastRunTime = now

			status, _ := c.getStatus()

			if status.CountdownRunning {
				status.CountdownNs -= deltaT
				status.SwitchOffAtStatus.Time = time.Now().Add(status.CountdownNs)
				status.SwitchOffAtStatus.Valid = true
			} else {
				status.SwitchOffAtStatus.Valid = false
			}

			if status.CountdownNs <= 0 {
				c.switchOff(status)
			}

			c.setStatus(status)

			c.emitStatus(status)
		}
	}
}
