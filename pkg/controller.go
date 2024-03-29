package coffee_ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/washed/shelly-go"

	"github.com/gin-gonic/gin"

	ks "github.com/washed/kitchen-sink-go"
	sse "github.com/washed/kitchen-sink-go/sse"
)

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
	friendlyName string
	apiRoot      string
	redisKeyRoot string
	router       *gin.Engine
	stream       *sse.Event
	redis        *redis.Client
	commandChan  chan func(status *Status)
	button       *shelly.ShellyButton1 // optional
	plugS        shelly.ShellyPlugS
	config       CoffeeControllerConfig
}

func NewCoffeeCtl(config CoffeeControllerConfig, router *gin.Engine) CoffeeCtl {
	s := sse.NewServer(nil)

	mqttOpts := ks.GetMQTTOpts()

	p := shelly.NewShellyPlugS(config.ShellyPlugSID, mqttOpts)

	var b_p *shelly.ShellyButton1 = nil
	if config.ShellyButton1ID != "" {
		b := shelly.NewShellyButton1(config.ShellyButton1ID, mqttOpts)
		b_p = &b
	}

	redisURL := os.Getenv("REDIS_URL")
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Error().Err(err).Str("redisURL", redisURL).Msg("error parsing REDIS_URL")
	}

	rdb := redis.NewClient(redisOptions)

	c := CoffeeCtl{
		friendlyName: config.Name,
		apiRoot:      config.APIRoot,
		redisKeyRoot: fmt.Sprintf("coffee-ctl:%s:", config.APIRoot),
		router:       router,
		stream:       s,
		button:       b_p,
		plugS:        p,
		redis:        rdb,
		commandChan:  make(chan func(status *Status), 100),
		config:       config,
	}

	return c
}

func (c *CoffeeCtl) makeRoutes() {
	c.router.GET(
		fmt.Sprintf("/%s/timer/stream", c.apiRoot),
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
	c.router.POST(
		fmt.Sprintf("/%s/timer", c.apiRoot),
		func(ctx *gin.Context) {
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

	c.router.POST(
		fmt.Sprintf("/%s/on", c.apiRoot),
		func(ctx *gin.Context) {
			c.commandChan <- c.switchOn
			ctx.Status(http.StatusOK)
		})

	c.router.POST(
		fmt.Sprintf("/%s/off", c.apiRoot),
		func(ctx *gin.Context) {
			c.commandChan <- c.switchOff
			ctx.Status(http.StatusOK)
		})
}

func (c *CoffeeCtl) getStatus() (*Status, error) {
	ctx := context.Background()
	statusStr, err := c.redis.Get(ctx, c.redisKeyRoot+"status").Result()
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
	err = c.redis.Set(ctx, c.redisKeyRoot+"status", statusBytes, time.Hour).Err()
	if err != nil {
		log.Panic().Err(err).Msg("error writing status to redis")
	}
}

func (c *CoffeeCtl) switchOn(status *Status) {
	log.Debug().Msg("switching on, starting countdown")
	c.plugS.SwitchOn()
	status.IntendedSwitchState = true
}

func (c *CoffeeCtl) switchOff(status *Status) {
	log.Debug().Msg("switching off")
	c.plugS.SwitchOff()
	status.IntendedSwitchState = false
}

func (c *CoffeeCtl) subscribeSwitchState() {
	c.plugS.SubscribeRelayState(func() {
		c.commandChan <- func(status *Status) {
			if !status.SwitchState {
				status.SwitchState = true
				status.CountdownRunning = true
			}
		}
	}, func() {
		c.commandChan <- func(status *Status) {
			if status.SwitchState {
				status.SwitchState = false
				status.IntendedSwitchState = false
				status.CountdownRunning = false
				status.CountdownNs = c.config.DefaultCountdown
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

func (c *CoffeeCtl) Connect() {
	c.plugS.Connect()
	if c.button != nil {
		c.button.Connect()
	}
}

func (c *CoffeeCtl) Close() {
	c.plugS.Close()
	if c.button != nil {
		c.button.Close()
	}
}

func (c *CoffeeCtl) Run() {
	c.subscribeSwitchState()
	if c.button != nil {
		c.subscribeButtonEvents()
		c.subscribeButtonBattery()
	}

	c.makeRoutes()

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
