// Package peermgr uses channels to simulate the queue handler with the actor model.
// A suitable number k ,should be set for channel size, because if #numOfMsg > k, we lose determinism.
// k chosen should be large enough that when filled, it shall indicate that the peer has stopped
// responding, since we do not have a pingMSG, we will need another way to shut down peers.
package peermgr

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.dusk.network/dusk-core/dusk-go/pkg/crypto"

	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/peer/stall"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/protocol"
)

const (
	maxOutboundConnections = 100
	handshakeTimeout       = 30 * time.Second
	idleTimeout            = 2 * time.Minute // If no message received after idleTimeout, then peer disconnects

	// nodes will have `responseTime` seconds to reply with a response
	responseTime = 300 * time.Second

	// the stall detector will check every `tickerInterval` to see if messages
	// are overdue. Should be less than `responseTime`
	tickerInterval = 30 * time.Second

	// The input buffer size is the amount of mesages that
	// can be buffered into the channel to receive at once before
	// blocking, and before determinism is broken
	inputBufferSize = 100

	// The output buffer size is the amount of messages that
	// can be buffered into the channel to send at once before
	// blocking, and before determinism is broken.
	outputBufferSize = 100

	// pingInterval = 20 * time.Second //Not implemented in Dusk clients
)

var (
	errHandShakeTimeout    = errors.New("Handshake timed out, peers have " + handshakeTimeout.String() + " to complete the handshake")
	errHandShakeFromStr    = "Handshake failed: %s"
	receivedMessageFromStr = "Received a '%s' message from %s"
)

// Peer holds all configuration and state to be able to communicate with other peers.
// Every Peer has a Detector that keeps track of pending messages that require a synchronous response.
type Peer struct {
	// Unchangeable state: concurrent safe
	Addr      string
	ProtoVer  *protocol.Version
	Inbound   bool
	UserAgent string
	Services  protocol.ServiceFlag
	CreatedAt time.Time
	Relay     bool

	Conn net.Conn

	eventBus *wire.EventBus

	cfg *Config

	// Atomic vals
	Disconnected int32

	statemutex     sync.Mutex
	VerackReceived bool
	VersionKnown   bool

	*stall.Detector

	inch   chan func() // Will handle all inbound connections from peer
	outch  chan func() // Will handle all outbound connections to peer
	quitch chan struct{}
}

// NewPeer is called after a connection to a peer was successful.
// Inbound as well as Outbound.
func NewPeer(conn net.Conn, inbound bool, cfg *Config, eventBus *wire.EventBus) *Peer {
	p := &Peer{
		inch:     make(chan func(), inputBufferSize),
		outch:    make(chan func(), outputBufferSize),
		quitch:   make(chan struct{}, 1),
		Inbound:  inbound,
		Conn:     conn,
		eventBus: eventBus,
		Addr:     conn.RemoteAddr().String(),
		Detector: stall.NewDetector(responseTime, tickerInterval),
		cfg:      cfg,
	}

	return p
}

// Write to a peer
func (p *Peer) Write(msg wire.Payload) error {
	return wire.WriteMessage(p.Conn, p.cfg.Magic, msg)
}

// Disconnect disconnects from a peer
func (p *Peer) Disconnect() {
	// return if already disconnected
	if atomic.LoadInt32(&p.Disconnected) != 0 {
		return
	}

	atomic.AddInt32(&p.Disconnected, 1)

	p.Detector.Quit()
	close(p.quitch)
	p.Conn.Close()
}

// Net returns the protocol magic
func (p *Peer) Net() protocol.Magic {
	return p.cfg.Magic
}

// Port returns the port
func (p *Peer) Port() uint16 {
	s := strings.Split(p.Conn.RemoteAddr().String(), ":")
	port, _ := strconv.ParseUint(s[1], 10, 16)
	return uint16(port)
}

//End of Exposed API functions//

// Read from a peer
func (p *Peer) readHeader() (*MessageHeader, error) {
	headerBytes, err := p.readHeaderBytes()
	if err != nil {
		return nil, err
	}

	headerBuffer := bytes.NewReader(headerBytes)

	header, err := decodeMessageHeader(headerBuffer)
	if err != nil {
		return nil, err
	}

	return header, nil
}

func (p *Peer) readHeaderBytes() ([]byte, error) {
	buffer := make([]byte, MessageHeaderSize)
	if _, err := io.ReadFull(p.Conn, buffer); err != nil {
		return nil, err
	}

	return buffer, nil
}

func (p *Peer) readPayload(length uint32) (*bytes.Buffer, error) {
	buffer := make([]byte, length)
	if _, err := io.ReadFull(p.Conn, buffer); err != nil {
		return nil, err
	}

	return bytes.NewBuffer(buffer), nil
}

func (p *Peer) headerMagicIsValid(header *MessageHeader) bool {
	return p.cfg.Magic == header.Magic
}

func payloadChecksumIsValid(payloadBuffer *bytes.Buffer, checksum uint32) bool {
	return crypto.CompareChecksum(payloadBuffer.Bytes(), checksum)
}

// PingLoop not implemented yet.
// Will cause this client to disconnect from all other implementations
func (p *Peer) PingLoop() { /*not implemented in other neo clients*/ }

// Run is used to start communicating with the peer, completes the handshake and starts observing
// for messages coming in
func (p *Peer) Run() error {

	// err := p.Handshake()

	go p.StartProtocol()
	go p.ReadLoop()
	go p.WriteLoop()

	//go p.PingLoop() // since it is not implemented. It will disconnect all other impls.
	return nil

}

// StartProtocol is run as a go-routine, will act as our queue for messages.
// Should be ran after handshake
func (p *Peer) StartProtocol() {
loop:
	for atomic.LoadInt32(&p.Disconnected) == 0 {
		select {
		case f := <-p.inch:
			f()
		case <-p.quitch:
			break loop
		case <-p.Detector.Quitch:
			break loop
		}
	}
	p.Disconnect()
}

// ReadLoop will block on the read until a message is read.
// Should only be called after handshake is complete on a seperate go-routine.
func (p *Peer) ReadLoop() {

	idleTimer := time.AfterFunc(idleTimeout, func() {
		p.Disconnect()
	})

	for atomic.LoadInt32(&p.Disconnected) == 0 {

		idleTimer.Reset(idleTimeout) // reset timer on each loop

		header, err := p.readHeader()
		idleTimer.Stop()
		if err != nil {
			// This will also happen if Peer is disconnected
			p.Disconnect()
			return
		}

		if p.headerMagicIsValid(header) {
			payloadBuffer, err := p.readPayload(header.Length)
			if err != nil {
				p.Disconnect()
				return
			}

			if payloadChecksumIsValid(payloadBuffer, header.Checksum) {
				p.eventBus.Publish(string(header.Command), payloadBuffer)
			}
		}
	}
}

// WriteLoop will queue all messages to be written to the peer
func (p *Peer) WriteLoop() {
	for atomic.LoadInt32(&p.Disconnected) == 0 {
		select {
		case f := <-p.outch:
			f()
		case <-p.Detector.Quitch: // if the detector quits, disconnect peer
			p.Disconnect()
		}
	}
}

func (p *Peer) WriteConsensus(msg wire.Payload) error {
	p.outch <- func() {
		p.Write(msg)
	}
	return nil
}
