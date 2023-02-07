package controller

import (
	"coffee-ctl/pkg/config"
	"coffee-ctl/pkg/mqttopts"
	"coffee-ctl/pkg/sse"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
	"github.com/washed/shelly-go"

	"github.com/gin-gonic/gin"
)

const defaultCountdownNs = 2 * time.Hour
const mutexName = "coffee-ctl:status:mutex"

func NewCoffeeCtl(conf config.Config) (c CoffeeCtl) {
	r := gin.Default()
	s := sse.NewServer(c.GetStatusString)

	mqttOpts := mqttopts.GetMQTTOpts()
	b := shelly.NewShellyButton1(conf.ShellyButton1ID, mqttOpts)
	p := shelly.NewShellyPlugS(conf.ShellyPlugSID, mqttOpts)

	// TODO: get redis DSN from env
	rdb := redis.NewClient(&redis.Options{
		Addr:     "redis:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	pool := goredis.NewPool(rdb)
	rs := redsync.New(pool)
	mutex := rs.NewMutex(mutexName)

	c = CoffeeCtl{
		router: r,
		stream: s,
		button: b,
		plugS:  p,
		redis:  rdb,
		mutex:  mutex,
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
	LastRunTime         time.Time           `json:"lastRunTime"`
	CountdownNs         time.Duration       `json:"countdownNs"`
	SwitchOffAtStatus   SwitchOffAtStatus   `json:"switchOffAtStatus"`
	CountdownRunning    bool                `json:"countdownRunning"`
	IntendedSwitchState bool                `json:"intendedSwitchState"`
	SwitchState         bool                `json:"switchState"`
	ButtonBatteryStatus ButtonBatteryStatus `json:"buttonBatteryStatus"`
}

type CoffeeCtl struct {
	router *gin.Engine
	stream *sse.Event
	redis  *redis.Client
	mutex  *redsync.Mutex
	button shelly.ShellyButton1
	plugS  shelly.ShellyPlugS
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

		ctx.Status(http.StatusOK)
	})

	c.router.POST("/on", func(ctx *gin.Context) {
		c.SwitchOn()
		ctx.Status(http.StatusOK)
	})

	c.router.POST("/off", func(ctx *gin.Context) {
		c.SwitchOff()
		ctx.Status(http.StatusOK)
	})
}

func (c *CoffeeCtl) lock() {
	if err := c.mutex.Lock(); err != nil {
		log.Panic().Err(err).Msg("error locking mutex")
	}
}

func (c *CoffeeCtl) unlock() {
	if ok, err := c.mutex.Unlock(); !ok || err != nil {
		log.Panic().Err(err).Msg("error unlocking mutex")
	}
}

func (c *CoffeeCtl) getStatus() (*Status, error) {
	ctx := context.Background()
	statusStr, err := c.redis.Get(ctx, "coffee-ctl:status").Result()
	if err == redis.Nil {
		log.Warn().Msg("no status found in redis")
		return nil, err
	} else if err != nil {
		log.Panic().Err(err).Msg("error reading status from redis")
		return nil, err
	} else {
		var status Status
		err = json.Unmarshal([]byte(statusStr), &status)
		log.Debug().Interface("status", status).Msg("status from redis")
		if err != nil {
			log.Error().Err(err).Msg("error unmarshalling status")
		}
		return &status, nil
	}
}

func (c *CoffeeCtl) atomicStatusReadModifyWrite(fn func(status *Status), readonly bool) *Status {
	ctx := context.Background()

	c.lock()

	status, err := c.getStatus()
	if err != nil {
		log.Error().Err(err).Msg("error getting status")
		c.unlock()
		return nil
	}

	fn(status)

	if !readonly {
		err := c.redis.Set(ctx, "coffee-ctl:status", c.GetStatusString(), time.Hour).Err()
		if err != nil {
			log.Panic().Err(err).Msg("error writing status to redis")
		}
	}

	c.unlock()

	return status
}

func (c *CoffeeCtl) GetStatusString() string {
	status := c.atomicStatusReadModifyWrite(func(status *Status) {}, true)
	payload, _ := json.Marshal(status)

	// TODO: error handling?
	return string(payload)
}

func (c *CoffeeCtl) SwitchOn() {
	log.Debug().Msg("switching on, starting countdown")
	c.plugS.SwitchOn()
	c.atomicStatusReadModifyWrite(func(status *Status) {
		status.IntendedSwitchState = true
		status.CountdownRunning = true
	}, false)
}

func (c *CoffeeCtl) SwitchOff() {
	log.Debug().Msg("switching off, stopping countdown")
	c.plugS.SwitchOff()
	c.atomicStatusReadModifyWrite(func(status *Status) {
		status.IntendedSwitchState = false
		status.CountdownRunning = false
		status.CountdownNs = defaultCountdownNs
	}, false)
}

func (c *CoffeeCtl) subscribeSwitchState() {
	/*
		We call SwitchOn/Off here to correctly track state if the plug is turned on
		via the physical button on the plug or the web UI
	*/
	c.plugS.SubscribeRelayState(func() {
		c.atomicStatusReadModifyWrite(func(status *Status) {
			if !status.SwitchState {
				status.SwitchState = true
				c.SwitchOn()
			}
		}, false)
	}, func() {
		c.atomicStatusReadModifyWrite(func(status *Status) {
			if status.SwitchState {
				status.SwitchState = false
				c.SwitchOff()
			}
		}, false)
	})
}

func (c *CoffeeCtl) subscribeButtonEvents() {
	c.button.SubscribeInputEvent(func() {
		c.atomicStatusReadModifyWrite(func(status *Status) {
			if status.SwitchState {
				c.SwitchOff()
			} else {
				c.SwitchOn()
			}
		}, false)
	}, nil, nil, nil)
}

func (c *CoffeeCtl) subscribeButtonBattery() {
	c.atomicStatusReadModifyWrite(func(status *Status) {
		status.ButtonBatteryStatus.Valid = false
	}, false)

	c.button.SubscribeBattery(func(buttonBatterySoC float32) {
		c.atomicStatusReadModifyWrite(func(status *Status) {
			status.ButtonBatteryStatus.Soc = buttonBatterySoC
			status.ButtonBatteryStatus.Valid = true
		}, false)
	})
}

func (c *CoffeeCtl) AddTime(d time.Duration) {
	c.atomicStatusReadModifyWrite(func(status *Status) {
		status.CountdownNs += d
		if status.CountdownNs < 0 {
			status.CountdownNs = 0
		}

		log.Debug().
			Int("added duration", int(d)).
			Int("new CountdownNs", int(status.CountdownNs)).
			Msg("adding time to CountdownNs")
	}, false)
}

func (c *CoffeeCtl) emitStatus(status *Status) {
	log.Debug().
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

	var deltaT time.Duration = 0
	var lastRunTime time.Time

	for {
		c.atomicStatusReadModifyWrite(func(status *Status) {
			if lastRunTime.IsZero() {
				lastRunTime = time.Now()
			} else {
				deltaT = time.Since(lastRunTime)
				lastRunTime = time.Now()
			}
			status.LastRunTime = lastRunTime

			log.Trace().Dur("deltaT", deltaT).Msg("running coffee-ctl loop")

			if status.CountdownRunning {
				status.CountdownNs -= deltaT
				status.SwitchOffAtStatus.Time = time.Now().Add(status.CountdownNs)
				status.SwitchOffAtStatus.Valid = true
			} else {
				status.SwitchOffAtStatus.Valid = false
			}

			if status.CountdownNs <= 0 {
				c.SwitchOff()
			}

			c.emitStatus(status)
		}, false)

		runTime := time.Since(lastRunTime)
		log.Trace().Dur("runTime", runTime).Msg("controller loop")
		time.Sleep(200*time.Millisecond - runTime)
	}
}
