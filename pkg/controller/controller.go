package controller

import (
	"coffee-ctl/pkg/config"
	"coffee-ctl/pkg/mqttopts"
	"coffee-ctl/pkg/sse"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/washed/shelly-go"

	"github.com/gin-gonic/gin"
)

const defaultCountdownNs = 2 * time.Hour

func NewCoffeeCtl(conf config.Config) (c CoffeeCtl) {
	r := gin.Default()
	s := sse.NewServer(c.GetStatusString)

	mqttOpts := mqttopts.GetMQTTOpts()
	b := shelly.NewShellyButton1(conf.ShellyButton1ID, mqttOpts)
	p := shelly.NewShellyPlugS(conf.ShellyPlugSID, mqttOpts)

	c = CoffeeCtl{
		router: r,
		stream: s,
		button: b,
		plugS:  p,
		status: Status{
			CountdownNs: defaultCountdownNs,
		},
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
	button      shelly.ShellyButton1
	plugS       shelly.ShellyPlugS
	lastRunTime time.Time
	status      Status
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

		c.AddTime(timer.Delta)

		ctx.JSON(http.StatusOK, c.status)
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

func (c *CoffeeCtl) GetStatusString() string {
	payload, _ := json.Marshal(c.status)
	// TODO: error handling?
	return string(payload)
}

func (c *CoffeeCtl) startCountdown() {
	log.Debug().Msg("starting countdown")
	c.status.CountdownRunning = true
}

func (c *CoffeeCtl) stopCountdown() {
	log.Debug().Msg("stopping countdown")
	c.status.CountdownRunning = false
	c.status.CountdownNs = defaultCountdownNs
}

func (c *CoffeeCtl) SwitchOn() {
	log.Debug().Msg("switching on")
	c.plugS.SwitchOn()
	c.status.IntendedSwitchState = true
	c.startCountdown()
}

func (c *CoffeeCtl) SwitchOff() {
	log.Debug().Msg("switching off")
	c.plugS.SwitchOff()
	c.status.IntendedSwitchState = false
	c.stopCountdown()
}

func (c *CoffeeCtl) subscribeSwitchState() {
	/*
		We call SwitchOn/Off here to correctly track state if the plug is turned on
		via the physical button on the plug or the web UI
	*/
	c.plugS.SubscribeRelayState(func() {
		if !c.status.SwitchState {
			c.status.SwitchState = true
			c.SwitchOn()
		}
	}, func() {
		if c.status.SwitchState {
			c.status.SwitchState = false
			c.SwitchOff()
		}
	})
}

func (c *CoffeeCtl) subscribeButtonEvents() {
	c.button.SubscribeInputEvent(func() {
		if c.status.SwitchState {
			c.SwitchOff()
		} else {
			c.SwitchOn()
		}
	}, nil, nil, nil)
}

func (c *CoffeeCtl) subscribeButtonBattery() {
	c.status.ButtonBatteryStatus.Valid = false

	c.button.SubscribeBattery(func(buttonBatterySoC float32) {
		c.status.ButtonBatteryStatus.Soc = buttonBatterySoC
		c.status.ButtonBatteryStatus.Valid = true
	})
}

func (c *CoffeeCtl) AddTime(d time.Duration) {
	c.status.CountdownNs += d
	if c.status.CountdownNs < 0 {
		c.status.CountdownNs = 0
	}

	log.Debug().
		Int("added duration", int(d)).
		Int("new CountdownNs", int(c.status.CountdownNs)).
		Msg("adding time to CountdownNs")
}

func (c *CoffeeCtl) emitStatus() {
	log.Debug().
		Interface("status", c.status).
		Msg("emitting status")

	payload, _ := json.Marshal(c.status)
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

	var deltaT time.Duration = 0
	for {
		if c.lastRunTime.IsZero() {
			c.lastRunTime = time.Now()
		} else {
			deltaT = time.Since(c.lastRunTime)
			c.lastRunTime = time.Now()
		}

		log.Trace().Int("deltaT", int(deltaT)).Msg("running coffee-ctl loop")

		if c.status.CountdownRunning {
			c.status.CountdownNs -= deltaT
			c.status.SwitchOffAtStatus.Time = time.Now().Add(c.status.CountdownNs)
			c.status.SwitchOffAtStatus.Valid = true
		} else {
			c.status.SwitchOffAtStatus.Valid = false
		}

		if c.status.CountdownNs <= 0 {
			c.SwitchOff()
		}

		c.emitStatus()

		time.Sleep(200 * time.Millisecond)
	}
}
