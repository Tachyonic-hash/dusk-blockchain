package eventbus

import (
	"bytes"
	"math/rand"
	"sync"

	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/topics"
)

// Preprocessor is for mutating a message before it gets notified to the subscribers of a topic
type Preprocessor interface {
	Process(*bytes.Buffer) error
	// TODO: processors should have a preassigned ID with a priority to avoid processing duplication
}

// ProcessorRegistry is a registry of TopicProcessor
type ProcessorRegistry interface {
	Preprocess(topics.Topic, *bytes.Buffer) error
	Register(topics.Topic, ...Preprocessor) []uint32
	RemoveProcessor(topics.Topic, uint32)
	RemoveProcessors(topics.Topic)
	RemoveAllProcessors()
}

var _ ProcessorRegistry = (*SafeProcessorRegistry)(nil)

// SafeProcessorRegistry allows registration of preprocessors to be applied to incoming Event on a specific topic
// It is threadsafe
type SafeProcessorRegistry struct {
	sync.RWMutex
	preprocessors map[topics.Topic][]idProcessor
}

// NewSafeProcessorRegistry creates a new Preprocessor
func NewSafeProcessorRegistry() ProcessorRegistry {
	return &SafeProcessorRegistry{
		preprocessors: make(map[topics.Topic][]idProcessor),
	}
}

// Preprocess applies to a message all preprocessors registered for a topic
func (p *SafeProcessorRegistry) Preprocess(topic topics.Topic, messageBuffer *bytes.Buffer) error {
	p.RLock()
	pp := p.preprocessors[topic]
	p.RUnlock()

	for _, preprocessor := range pp {
		if err := preprocessor.Process(messageBuffer); err != nil {
			return err
		}
	}

	return nil
}

// Register creates a new set of TopicProcessor to a specified topic.
func (p *SafeProcessorRegistry) Register(topic topics.Topic, preprocessors ...Preprocessor) []uint32 {
	pproc, pprocIds := wrapInIDTopic(topic, preprocessors)

	p.Lock()
	defer p.Unlock()
	if _, ok := p.preprocessors[topic]; ok {
		p.preprocessors[topic] = append(p.preprocessors[topic], pproc...)
		return pprocIds
	}

	p.preprocessors[topic] = pproc
	return pprocIds
}

//wrapInIDTopic is a convenient function to wrap a slice of TopicProcessor into a slice of idProcessor
func wrapInIDTopic(topic topics.Topic, p []Preprocessor) ([]idProcessor, []uint32) {
	pproc := make([]idProcessor, len(p))
	pprocIds := make([]uint32, len(p))
	for i := 0; i < len(p); i++ {
		id := rand.Uint32()
		pprocIds[i] = id
		pproc[i] = idProcessor{
			Preprocessor: p[i],
			id:           id,
		}
	}
	return pproc, pprocIds
}

// RemoveProcessor removes all TopicProcessor previously registered on a given topic using its ID
func (p *SafeProcessorRegistry) RemoveProcessor(topic topics.Topic, id uint32) {
	p.Lock()
	defer p.Unlock()
	if pprocs, ok := p.preprocessors[topic]; ok {
		for idx, preproc := range pprocs {
			if preproc.id == id {
				// remove the item
				pprocs = append(pprocs[:idx], pprocs[idx+1:]...)
				p.preprocessors[topic] = pprocs
				return
			}
		}
	}
}

// RemoveProcessors removes all TopicProcessor from a topic
func (p *SafeProcessorRegistry) RemoveProcessors(topic topics.Topic) {
	p.Lock()
	defer p.Unlock()
	delete(p.preprocessors, topic)
}

// RemoveAllProcessors removes all TopicProcessors from all topics
func (p *SafeProcessorRegistry) RemoveAllProcessors() {
	p.Lock()
	defer p.Unlock()
	for topic := range p.preprocessors {
		delete(p.preprocessors, topic)
	}
}
