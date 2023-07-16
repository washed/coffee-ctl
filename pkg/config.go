package coffee_ctl

import (
	"time"

	ks "github.com/washed/kitchen-sink-go"
)

type CoffeeControllerConfig struct {
	Name               string        `yaml:"name"`
	APIRoot            string        `yaml:"api_root"`
	ShellyPlugSID      string        `yaml:"shelly_plug_s_id"`
	ShellyButton1ID    string        `yaml:"shelly_button1_id"`
	defaultCountdownNs time.Duration `yaml:"default_countdown_ns"`
}

type Config struct {
	Log               ks.LogConfig             `yaml:"log"`
	CoffeeControllers []CoffeeControllerConfig `yaml:"coffee_controllers"`
}
