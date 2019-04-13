package wire

import (
	"bytes"

	log "github.com/sirupsen/logrus"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/topics"
)

// QuitTopic is the topic to make all components quit
const QuitTopic = "quit"

type (

	// The Event is an Entity that represents the Messages travelling on the EventBus.
	// It would normally present always the same fields.
	Event interface {
		Sender() []byte
		Equal(Event) bool
	}

	// EventUnmarshaller unmarshals an Event from a buffer. Following Golang's way of
	// defining interfaces, it exposes an Unmarshal method which allows for flexibility
	// and reusability across all the different components that need to read the buffer
	// coming from the EventBus into different structs
	EventUnmarshaller interface {
		Unmarshal(*bytes.Buffer, Event) error
	}

	// EventMarshaller is the specular operation of an EventUnmarshaller. Following
	// Golang's way of defining interfaces, it exposes an Unmarshal method which allows
	// for flexibility and reusability across all the different components that need to
	// read the buffer coming from the EventBus into different structs
	EventMarshaller interface {
		Marshal(*bytes.Buffer, Event) error
	}

	// EventUnMarshaller is a convenient interface providing both Marshalling and
	// Unmarshalling capabilities
	EventUnMarshaller interface {
		EventMarshaller
		EventUnmarshaller
	}

	// EventPrioritizer is used by the EventSelector to prioritize events
	// (normally to return the best collected after a timespan)
	EventPrioritizer interface {
		Priority(Event, Event) Event
	}

	// EventVerifier is the interface to verify an Event
	EventVerifier interface {
		Verify(Event) error
	}

	// EventCollector is the interface for collecting Events. Pretty much processors
	// involves some degree of Event collection (either until a Quorum is reached or
	// until a Timeout). This Interface is typically implemented by a struct that will
	// perform some Event unmarshalling.
	EventCollector interface {
		Collect(*bytes.Buffer) error
	}

	TopicProcessor interface {
		Process(*bytes.Buffer) error
	}

	// TopicListener accepts events from the EventBus and takes care of reacting on
	// quit Events. It delegates the business logic to the EventCollector which is
	// supposed to handle the incoming events
	TopicListener struct {
		subscriber     EventSubscriber
		eventCollector EventCollector
		msgChan        <-chan *bytes.Buffer
		MsgChanID      uint32
		quitChan       <-chan *bytes.Buffer
		QuitChanID     uint32
		topic          string
	}

	// EventSubscriber subscribes a channel to Event notifications on a specific topic
	EventSubscriber interface {
		Subscribe(string, chan<- *bytes.Buffer) uint32
		Unsubscribe(string, uint32) bool
	}

	// EventPublisher publishes serialized messages on a specific topic
	EventPublisher interface {
		Publish(string, *bytes.Buffer)
	}

	// EventBroker is an EventPublisher and an EventSubscriber
	EventBroker interface {
		EventSubscriber
		EventPublisher
	}
)

// NewTopicListener creates the TopicListener listening to a topic on the EventBus.
// The EventBus, EventCollector and Topic are injected
func NewTopicListener(subscriber EventSubscriber, collector EventCollector, topic string) *TopicListener {

	quitChan := make(chan *bytes.Buffer, 1)
	msgChan := make(chan *bytes.Buffer, 100)

	msgChanID := subscriber.Subscribe(topic, msgChan)
	quitChanID := subscriber.Subscribe(string(QuitTopic), quitChan)

	return &TopicListener{
		subscriber:     subscriber,
		msgChan:        msgChan,
		MsgChanID:      msgChanID,
		quitChan:       quitChan,
		QuitChanID:     quitChanID,
		topic:          topic,
		eventCollector: collector,
	}
}

// Accept incoming (mashalled) Events on the topic of interest and dispatch them to the EventCollector.Collect. It accepts a variadic number of TopicProcessors which pre-process the buffer before passing it to the Collector
func (n *TopicListener) Accept(processors ...TopicProcessor) {
	log.WithFields(log.Fields{
		"id":    n.MsgChanID,
		"topic": n.topic,
	}).Debugln("Accepting messages")
	for {
		select {
		case <-n.quitChan:
			n.subscriber.Unsubscribe(n.topic, n.MsgChanID)
			n.subscriber.Unsubscribe(string(QuitTopic), n.QuitChanID)
			return
		case eventBuffer := <-n.msgChan:
			if err := preprocess(eventBuffer, processors...); err != nil {
				log.WithError(err).WithFields(
					log.Fields{
						"process": "topic listner",
						"topic":   n.topic,
					}).Warnln("processor failure")
				continue
			}

			if len(n.msgChan) > 10 {
				log.WithFields(log.Fields{
					"id":         n.MsgChanID,
					"topic":      n.topic,
					"Unconsumed": len(n.msgChan),
				}).Debugln("Channel is accumulating messages")
			}
			if err := n.eventCollector.Collect(eventBuffer); err != nil {
				log.WithFields(log.Fields{
					"id":         n.MsgChanID,
					"topic":      n.topic,
					"Unconsumed": len(n.msgChan),
				}).Warnln("Channel is accumulating messages")
			}
		}
	}
}

func preprocess(eventBuffer *bytes.Buffer, processors ...TopicProcessor) error {
	for _, processor := range processors {
		if err := processor.Process(eventBuffer); err != nil {
			return err
		}
	}
	return nil
}

// AddTopic is a convenience function to add a specified topic at the start of
// a buffer. This topic is later decoded by the peer when gossiping messages,
// to be put on the message header.
func AddTopic(m *bytes.Buffer, topic topics.Topic) (*bytes.Buffer, error) {
	buffer := new(bytes.Buffer)
	topicBytes := topics.TopicToByteArray(topic)
	if _, err := buffer.Write(topicBytes[:]); err != nil {
		return nil, err
	}

	if _, err := buffer.Write(m.Bytes()); err != nil {
		return nil, err
	}

	return buffer, nil
}
