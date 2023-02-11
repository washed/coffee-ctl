package main

import (
	"os"
	"syscall"

	"os/signal"

	cc "coffee-ctl/pkg"

	"github.com/rs/zerolog/log"
	ks "github.com/washed/kitchen-sink-go"
)

func main() {
	config := cc.Config{}
	ks.ReadConfig(&config)
	ks.InitLogger(config.Log)

	c := cc.NewCoffeeCtl(config)

	go c.Run()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	signal.Notify(sig, syscall.SIGTERM)

	<-sig
	log.Info().Msg("Exiting")
}
