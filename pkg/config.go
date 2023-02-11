package coffee_ctl

import ks "github.com/washed/kitchen-sink-go"

type Config struct {
	Log             ks.LogConfig `yaml:"log"`
	ShellyPlugSID   string       `yaml:"shelly_plug_s_id"`
	ShellyButton1ID string       `yaml:"shelly_button1_id"`
}
