package mqtt

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/gaucho-racing/blix/runway/config"
)

type Publisher struct {
	connection *autopaho.ConnectionManager
}

func Connect(ctx context.Context, configuration config.Config) (*Publisher, error) {
	serverURL, err := url.Parse(configuration.MQTTURL)
	if err != nil {
		return nil, fmt.Errorf("parse MQTT URL: %w", err)
	}
	connection, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{serverURL},
		KeepAlive:                     20,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		ConnectRetryDelay:             time.Second,
		ClientConfig: paho.ClientConfig{
			ClientID: configuration.MQTTClientID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create MQTT connection: %w", err)
	}
	if err := connection.AwaitConnection(ctx); err != nil {
		return nil, fmt.Errorf("connect to MQTT broker: %w", err)
	}
	return &Publisher{connection: connection}, nil
}

func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error {
	_, err := p.connection.Publish(ctx, &paho.Publish{
		QoS:     1,
		Topic:   topic,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("publish MQTT message: %w", err)
	}
	return nil
}

func (p *Publisher) Close(ctx context.Context) error {
	return p.connection.Disconnect(ctx)
}
