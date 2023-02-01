package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	LogLevel        string `yaml:"log_level"`
	ShellyPlugSID   string `yaml:"shelly_plug_s_id"`
	ShellyButton1ID string `yaml:"shelly_button1_id"`
}

func ReadConfig(file string, config *Config) error {
	yamlFile, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(yamlFile, config)
	if err != nil {
		return err
	}
	return nil
}

func WriteConfig(file string, config *Config) error {
	d, err := yaml.Marshal(&config)
	if err != nil {
		return err
	}
	err = os.WriteFile(file, d, 0644)
	if err != nil {
		return err
	}
	return nil
}
