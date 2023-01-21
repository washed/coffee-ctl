package main

import (
	"coffee-ctl/pkg/config"
	"os"
	"syscall"
	"time"

	"os/signal"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func heartbeat() {
	heartbeatCounter := 0
	for {
		log.Debug().Int("heartbeatCounter", heartbeatCounter).Msg("heartbeat")
		heartbeatCounter++
		time.Sleep(time.Second)
	}
}

func main() {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
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

	go heartbeat()

	// do stuff here

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	signal.Notify(sig, syscall.SIGTERM)

	<-sig
	log.Info().Msg("Exiting")
}
