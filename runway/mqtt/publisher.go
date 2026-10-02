package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/gaucho-racing/blix/runway/config"
	"github.com/gaucho-racing/blix/runway/model"
	"github.com/gaucho-racing/blix/runway/pkg/logger"
)

type Publisher struct {
	connection *autopaho.ConnectionManager
}

func Connect(ctx context.Context, configuration config.Config, handleRunEvent func(model.Event)) (*Publisher, error) {
	serverURL, err := url.Parse(configuration.MQTTURL)
	if err != nil {
		return nil, fmt.Errorf("parse MQTT URL: %w", err)
	}
	subscriptionResult := make(chan error, 1)
	var initialSubscription sync.Once
	runEventTopic := configuration.MQTTRunEventTopicPrefix + "/+/events"
	connection, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{serverURL},
		KeepAlive:                     20,
		CleanStartOnInitialConnection: true,
		SessionExpiryInterval:         0,
		ConnectRetryDelay:             time.Second,
		OnConnectionUp: func(connection *autopaho.ConnectionManager, _ *paho.Connack) {
			subscribeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, subscribeErr := connection.Subscribe(subscribeCtx, &paho.Subscribe{
				Subscriptions: []paho.SubscribeOptions{{Topic: runEventTopic, QoS: 1}},
			})
			initialSubscription.Do(func() { subscriptionResult <- subscribeErr })
			if subscribeErr != nil {
				logger.SugarLogger.Errorf("subscribe to run events: %v", subscribeErr)
			}
		},
		ClientConfig: paho.ClientConfig{
			ClientID: configuration.MQTTClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(received paho.PublishReceived) (bool, error) {
					var event model.Event
					if err := json.Unmarshal(received.Packet.Payload, &event); err != nil {
						logger.SugarLogger.Errorf("decode run event from MQTT: %v", err)
						return true, nil
					}
					if event.Position == 0 || event.ID == "" || event.RunID == "" || event.SessionID == "" {
						logger.SugarLogger.Errorf("discard invalid run event from MQTT topic %s", received.Packet.Topic)
						return true, nil
					}
					handleRunEvent(event)
					return true, nil
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create MQTT connection: %w", err)
	}
	if err := connection.AwaitConnection(ctx); err != nil {
		return nil, fmt.Errorf("connect to MQTT broker: %w", err)
	}
	select {
	case err := <-subscriptionResult:
		if err != nil {
			return nil, fmt.Errorf("subscribe to run events: %w", err)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
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
