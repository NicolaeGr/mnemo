package config

import (
	"flag"
	"os"
	"path/filepath"
)

type Options struct {
	DataDir string
	Port    string
}

var Current Options

func Parse() {
	var flagDataDir string
	var flagPort string

	flag.StringVar(&flagDataDir, "data", "", "Custom path to the directory containing mnemo.db")
	flag.StringVar(&flagPort, "port", "8080", "HTTP port for the web interface and CardDAV engine")
	flag.Parse()

	if flagDataDir != "" {
		Current.DataDir = flagDataDir
	} else {
		if _, err := os.Stat("/var/lib"); err == nil {
			Current.DataDir = "/var/lib/mnemo"
		} else {
			userConfig, err := os.UserConfigDir()
			if err != nil {
				Current.DataDir = "./data"
			} else {
				Current.DataDir = filepath.Join(userConfig, "mnemo")
			}
		}
	}

	Current.Port = flagPort
}
