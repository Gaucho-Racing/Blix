package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment             string
	Port                    string
	DatabaseHost            string
	DatabasePort            string
	DatabaseUser            string
	DatabasePassword        string
	DatabaseName            string
	MQTTURL                 string
	MQTTClientID            string
	MQTTExecutionReadyTopic string
	OutboxPollInterval      time.Duration
	ExecutionLeaseDuration  time.Duration
	LeaseSweepInterval      time.Duration
}

func Load() (Config, error) {
	pollInterval, err := time.ParseDuration(value("OUTBOX_POLL_INTERVAL", "1s"))
	if err != nil || pollInterval <= 0 {
		return Config{}, fmt.Errorf("OUTBOX_POLL_INTERVAL must be a positive duration")
	}
	leaseDuration, err := time.ParseDuration(value("EXECUTION_LEASE_DURATION", "30s"))
	if err != nil || leaseDuration < 3*time.Second {
		return Config{}, fmt.Errorf("EXECUTION_LEASE_DURATION must be at least 3s")
	}
	leaseSweepInterval, err := time.ParseDuration(value("EXECUTION_LEASE_SWEEP_INTERVAL", "5s"))
	if err != nil || leaseSweepInterval <= 0 {
		return Config{}, fmt.Errorf("EXECUTION_LEASE_SWEEP_INTERVAL must be a positive duration")
	}
	port := value("PORT", "9999")
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return Config{}, fmt.Errorf("PORT must be a valid TCP port")
	}
	configuration := Config{
		Environment:             value("ENV", "PROD"),
		Port:                    port,
		DatabaseHost:            value("DATABASE_HOST", "localhost"),
		DatabasePort:            value("DATABASE_PORT", "5432"),
		DatabaseUser:            value("DATABASE_USER", "postgres"),
		DatabasePassword:        value("DATABASE_PASSWORD", "password"),
		DatabaseName:            value("DATABASE_NAME", "blix"),
		MQTTURL:                 value("MQTT_URL", "mqtt://localhost:1883"),
		MQTTClientID:            value("MQTT_CLIENT_ID", "blix-runway"),
		MQTTExecutionReadyTopic: value("MQTT_EXECUTION_READY_TOPIC", "blix/v1/executions/ready"),
		OutboxPollInterval:      pollInterval,
		ExecutionLeaseDuration:  leaseDuration,
		LeaseSweepInterval:      leaseSweepInterval,
	}
	if !strings.HasPrefix(configuration.MQTTURL, "mqtt://") && !strings.HasPrefix(configuration.MQTTURL, "mqtts://") {
		return Config{}, fmt.Errorf("MQTT_URL must use mqtt or mqtts")
	}
	return configuration, nil
}

func (c Config) IsProduction() bool {
	return c.Environment == "PROD"
}

func value(name string, fallback string) string {
	if configured := strings.TrimSpace(os.Getenv(name)); configured != "" {
		return configured
	}
	return fallback
}
