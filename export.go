package main

import (
	"context"

	"github.com/psyduck-etl/sdk"
	"github.com/rabbitmq/amqp091-go"
	"github.com/zclconf/go-cty/cty"
)

type queueConfig struct {
	Connection  string `cty:"connection"`
	Queue       string `cty:"queue"`
	ContentType string `cty:"content-type"`
	StopAfter   int    `cty:"stop-after"`
	ChunkSize   uint   `cty:"chunk-size"`
	NoWait      bool   `cty:"no-wait"`
	AutoAck     bool   `cty:"auto-ack"`
}

func connect(config *queueConfig) (*amqp091.Connection, *amqp091.Channel, amqp091.Queue, error) {
	conn, err := amqp091.Dial(config.Connection)
	if err != nil {
		return nil, nil, amqp091.Queue{}, err
	}

	channel, err := conn.Channel()
	if err != nil {
		return nil, nil, amqp091.Queue{}, err
	}

	// TODO sdk should support an object, where we would have queue declare options set
	// but for now, just defaults
	queue, err := channel.QueueDeclare(config.Queue, false, false, false, false, nil)
	if err != nil {
		return nil, nil, amqp091.Queue{}, err
	}

	return conn, channel, queue, nil
}

func disconnect(conn *amqp091.Connection, channel *amqp091.Channel, errs chan<- error) {
	if err := conn.Close(); err != nil {
		errs <- err
	}

	if err := channel.Close(); err != nil {
		errs <- err
	}
}

func Plugin() *sdk.Plugin {
	return &sdk.Plugin{
		Name: "amqp",
		Resources: []*sdk.Resource{
			{
				Kinds: sdk.PRODUCER | sdk.CONSUMER,
				Name:  "amqp-queue",
				Spec: []*sdk.Spec{
					{
						Name:        "connection",
						Description: "AMQP broker server connection string - amqp://{user}:{password}@{hostname}:{port}",
						Required:    true,
						Type:        cty.String,
					},
					{
						Name:        "queue",
						Description: "Name of the rmqp queue to interact with",
						Required:    true,
						Type:        cty.String,
					},
					{
						Name:        "content-type",
						Description: "Content type",
						Required:    false,
						Type:        cty.String,
						Default:     cty.StringVal("text/plain"),
					},
					{
						Name:        "stop-after",
						Description: "Stop after n iterations",
						Required:    false,
						Type:        cty.Number,
						Default:     cty.NumberIntVal(0),
					},
					{
						Name:        "chunk-size",
						Description: "Number of messages to get from the channel before ACK",
						Required:    false,
						Type:        cty.Number,
						Default:     cty.NumberUIntVal(1),
					},
					{
						Name:        "no-wait",
						Description: "TODO",
						Required:    false,
						Type:        cty.Bool,
						Default:     cty.BoolVal(false),
					},
					{
						Name:        "auto-ack",
						Description: "TODO",
						Required:    false,
						Type:        cty.Bool,
						Default:     cty.BoolVal(false),
					},
				},
				ProvideProducer: func(parse sdk.Parser) (sdk.Producer, error) {
					config := new(queueConfig)
					if err := parse(config); err != nil {
						return nil, err
					}

					conn, channel, queue, err := connect(config)
					if err != nil {
						return nil, err
					}

					// TODO if we encounter an err before we return data, errs, the function will deadlock if errs is unbuffered
					return func(send chan<- []byte, errs chan<- error) {
						messages, err := channel.Consume(queue.Name, "", config.AutoAck, false, false, config.NoWait, nil)
						if err != nil {
							errs <- err
						}

						iters := 0
						defer close(send)
						defer close(errs)
						defer disconnect(conn, channel, errs)

						for {
							msgBuf := make([]amqp091.Delivery, config.ChunkSize)
							for i := uint(0); i < config.ChunkSize; i++ {
								msgBuf[i] = <-messages
							}
							if !config.AutoAck {
								if err := msgBuf[len(msgBuf)-1].Ack(true); err != nil {
									errs <- err
									return
								}
							}
							for _, msg := range msgBuf {
								send <- msg.Body
								iters++
								if config.StopAfter != 0 && iters >= config.StopAfter {
									return
								}
							}
						}
					}, nil
				},
				ProvideConsumer: func(parse sdk.Parser) (sdk.Consumer, error) {
					config := new(queueConfig)
					if err := parse(config); err != nil {
						return nil, err
					}

					conn, channel, queue, err := connect(config)
					if err != nil {
						return nil, err
					}

					return func(recv <-chan []byte, errs chan<- error, done chan<- struct{}) {
						defer close(done)
						defer close(errs)
						defer disconnect(conn, channel, errs)
						for d := range recv {
							if err := channel.PublishWithContext(context.Background(), "", queue.Name, false, false, amqp091.Publishing{
								ContentType: config.ContentType,
								Body:        d,
							}); err != nil {
								errs <- err
							}
						}
					}, nil
				},
			},
		},
	}
}
