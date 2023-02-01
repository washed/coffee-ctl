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

const defaultTimeToSwitchOff = 2 * time.Hour

func NewCoffeeCtl(conf config.Config) (c CoffeeCtl) {
	r := gin.Default()
	s := sse.NewServer(c.GetStatusString)

	mqttOpts := mqttopts.GetMQTTOpts()
	b := shelly.NewShellyButton1(conf.ShellyButton1ID, mqttOpts)
	p := shelly.NewShellyPlugS(conf.ShellyPlugSID, mqttOpts)

	c = CoffeeCtl{
		timeToSwitchOff: defaultTimeToSwitchOff,
		router:          r,
		stream:          s,
		button:          b,
		plugS:           p,
	}

	return c
}

type Status struct {
	CountdownNs         time.Duration `json:"countdownNs"`
	SwitchOffAt         *time.Time    `json:"switchOffAt"`
	CountdownRunning    bool          `json:"countdownRunning"`
	IntendedSwitchState bool          `json:"intendedSwitchState"`
	SwitchState         bool          `json:"switchState"`
}

type CoffeeCtl struct {
	timeToSwitchOff     time.Duration
	router              *gin.Engine
	stream              *sse.Event
	button              shelly.ShellyButton1
	plugS               shelly.ShellyPlugS
	lastStatus          Status
	lastRunTime         time.Time
	countdownRunning    bool
	intendedSwitchState bool
	switchState         bool
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

func (c *CoffeeCtl) GetStatus() Status {
	var switchOffAtPtr *time.Time = nil
	if c.countdownRunning {
		switchOffAt := time.Now().Add(c.timeToSwitchOff)
		switchOffAtPtr = &switchOffAt
	}
	return Status{
		CountdownNs:         c.timeToSwitchOff,
		SwitchOffAt:         switchOffAtPtr,
		CountdownRunning:    c.countdownRunning,
		IntendedSwitchState: c.intendedSwitchState,
		SwitchState:         c.switchState,
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
	c.plugS.SwitchOn()
	c.intendedSwitchState = true
	c.startCountdown()
}

func (c *CoffeeCtl) SwitchOff() {
	log.Debug().Msg("switching off")
	c.plugS.SwitchOff()
	c.intendedSwitchState = false
	c.stopCountdown()
}

func (c *CoffeeCtl) subscribeSwitchState() {
	c.plugS.SubscribeRelayState(func() {
		c.switchState = true

		// We call SwitchOn/Off here to correctly track state if the plug is turned on
		// via the physical button on the plug or the web UI
		c.SwitchOn()
	}, func() {
		c.switchState = false
		c.SwitchOff()
	})
}

func (c *CoffeeCtl) subscribeButtonEvents() {
	c.button.SubscribeInputEvent(func() {
		if c.switchState {
			c.SwitchOff()
		} else {
			c.SwitchOn()
		}
	}, nil, nil, nil)
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
	c.plugS.Connect()
	defer c.plugS.Close()
	c.subscribeSwitchState()

	c.button.Connect()
	defer c.button.Close()
	c.subscribeButtonEvents()

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
