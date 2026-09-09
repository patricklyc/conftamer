package util

import (
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
)

func LoadConfig(file string, target interface{}) error {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("opening config file: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("unable to read config data: %v", err)
	}

	err = yaml.Unmarshal(data, target)
	if err != nil {
		return fmt.Errorf("unable to decode config file: %v", err)
	}

	return nil
}
