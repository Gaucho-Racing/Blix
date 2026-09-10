package config

import (
	"fmt"
	"net/url"
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
	MQTTRunEventTopicPrefix string
	OutboxPollInterval      time.Duration
	ExecutionLeaseDuration  time.Duration
	LeaseSweepInterval      time.Duration
	WorkerHeartbeatInterval time.Duration
	WorkerOfflineAfter      time.Duration
	SentinelURL             string
	SentinelClientID        string
	SentinelClientSecret    string
	SentinelScopes          string
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
	workerHeartbeatInterval, err := time.ParseDuration(value("WORKER_HEARTBEAT_INTERVAL", "5s"))
	if err != nil || workerHeartbeatInterval <= 0 {
		return Config{}, fmt.Errorf("WORKER_HEARTBEAT_INTERVAL must be a positive duration")
	}
	workerOfflineAfter, err := time.ParseDuration(value("WORKER_OFFLINE_AFTER", "15s"))
	if err != nil || workerOfflineAfter < 2*workerHeartbeatInterval {
		return Config{}, fmt.Errorf("WORKER_OFFLINE_AFTER must be at least twice WORKER_HEARTBEAT_INTERVAL")
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
		MQTTRunEventTopicPrefix: strings.TrimRight(value("MQTT_RUN_EVENT_TOPIC_PREFIX", "blix/v1/runs"), "/"),
		OutboxPollInterval:      pollInterval,
		ExecutionLeaseDuration:  leaseDuration,
		LeaseSweepInterval:      leaseSweepInterval,
		WorkerHeartbeatInterval: workerHeartbeatInterval,
		WorkerOfflineAfter:      workerOfflineAfter,
		SentinelURL:             strings.TrimRight(value("SENTINEL_URL", "https://sso.gauchoracing.com"), "/"),
		SentinelClientID:        value("SENTINEL_CLIENT_ID", ""),
		SentinelClientSecret:    value("SENTINEL_CLIENT_SECRET", ""),
		SentinelScopes:          value("SENTINEL_SCOPES", "user:read groups:read"),
	}
	if !strings.HasPrefix(configuration.MQTTURL, "mqtt://") && !strings.HasPrefix(configuration.MQTTURL, "mqtts://") {
		return Config{}, fmt.Errorf("MQTT_URL must use mqtt or mqtts")
	}
	if configuration.MQTTRunEventTopicPrefix == "" || strings.ContainsAny(configuration.MQTTRunEventTopicPrefix, "+#") {
		return Config{}, fmt.Errorf("MQTT_RUN_EVENT_TOPIC_PREFIX must be a concrete MQTT topic prefix")
	}
	if err := validateHTTPURL(configuration.SentinelURL); err != nil {
		return Config{}, fmt.Errorf("SENTINEL_URL %w", err)
	}
	if (configuration.SentinelClientID == "") != (configuration.SentinelClientSecret == "") {
		return Config{}, fmt.Errorf("SENTINEL_CLIENT_ID and SENTINEL_CLIENT_SECRET must be configured together")
	}
	if configuration.SentinelScopes == "" {
		return Config{}, fmt.Errorf("SENTINEL_SCOPES cannot be empty")
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

func validateHTTPURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("must be a valid HTTP or HTTPS URL")
	}
	return nil
}
