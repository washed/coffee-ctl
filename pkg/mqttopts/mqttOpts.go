package mqttopts

import (
	"os"
	"time"

	"github.com/rs/zerolog/log"

	MQTT "github.com/eclipse/paho.mqtt.golang"
)

func GetMQTTOpts() *MQTT.ClientOptions {
	mqttOpts := MQTT.NewClientOptions()
	mqttOpts.AddBroker(os.Getenv("MQTT_BROKER_URL"))
	mqttOpts.SetUsername(os.Getenv("MQTT_BROKER_USERNAME"))
	mqttOpts.SetPassword(os.Getenv("MQTT_BROKER_PASSWORD"))

	mqttOpts.SetOrderMatters(false)
	mqttOpts.ConnectTimeout = time.Second
	mqttOpts.WriteTimeout = time.Second
	mqttOpts.KeepAlive = 10
	mqttOpts.PingTimeout = time.Second
	mqttOpts.ConnectRetry = true
	mqttOpts.AutoReconnect = true

	mqttOpts.DefaultPublishHandler = func(_ MQTT.Client, msg MQTT.Message) {
		log.Warn().Interface("msg", msg).Msg("unexpected message")
	}

	mqttOpts.OnConnectionLost = func(cl MQTT.Client, err error) {
		log.Err(err).Msg("MQTT connection lost")
	}

	mqttOpts.OnReconnecting = func(MQTT.Client, *MQTT.ClientOptions) {
		log.Warn().Msg("MQTT attempting to reconnect")
	}

	return mqttOpts
}
