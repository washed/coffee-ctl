package main

import (
	"os"
	"syscall"

	"os/signal"

	cc "coffee-ctl/pkg"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	ks "github.com/washed/kitchen-sink-go"
)

func main() {
	config := cc.Config{}
	ks.ReadConfig(&config)
	ks.InitLogger(config.Log)

	r := gin.Default()
	r.SetTrustedProxies([]string{"0.0.0.0/24"})

	for _, coffeeControllerConfig := range config.CoffeeControllers {
		c := cc.NewCoffeeCtl(coffeeControllerConfig, r)
		c.Connect()
		defer c.Close()

		go c.Run()
	}

	go r.Run()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	signal.Notify(sig, syscall.SIGTERM)

	<-sig
	log.Info().Msg("Exiting")
}
