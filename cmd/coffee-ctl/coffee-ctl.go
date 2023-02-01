package main

import (
	"coffee-ctl/pkg/config"
	"coffee-ctl/pkg/controller"
	"os"
	"syscall"
	"time"

	"os/signal"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

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

	if conf.LogLevel == "" {
		conf.LogLevel = "info"
		log.Error().
			Str("conf.LogLevel", conf.LogLevel).
			Msg("using default log level")
	}

	logLevel, err := zerolog.ParseLevel(conf.LogLevel)
	if err != nil {
		log.Error().Str("conf.LogLevel", conf.LogLevel).Err(err).Msg("error configuring log level")
		os.Exit(1)
	}
	zerolog.SetGlobalLevel(logLevel)
	log.Error().
		Str("conf.LogLevel", conf.LogLevel).
		Int("logLevel", int(logLevel)).
		Msg("set log level")

	c := controller.NewCoffeeCtl(conf)

	go c.Run()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	signal.Notify(sig, syscall.SIGTERM)

	<-sig
	log.Info().Msg("Exiting")
}
