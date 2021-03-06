package p2p

import (
	"time"
	"log"
	"sync"
	"io"
	"sort"
	"os"
)

const (
	baseProtocolVersion    = 5
	baseProtocolLength     = uint64(16)
	baseProtocolMaxMsgSize = 2 * 1024
	snappyProtocolVersion  = 5
	pingInterval           = 15 * time.Second
)

type Peer struct {
	rw       *conn
	running  map[string]*protoRW
	log      log.Logger
	created  mclock.AbsTime
	wg       sync.WaitGroup
	protoErr chan error
	closed   chan struct{}
	disc     chan DiscReason // disconnect reason
	events   *event.Feed     // events receives message send / receive events if set
}

const (
	// devp2p message codes
	handshakeMsg = 0x00
	discMsg      = 0x01
	pingMsg      = 0x02
	pongMsg      = 0x03
	getPeersMsg  = 0x04
	peersMsg     = 0x05
)

type protoRW struct {
	Protocol
	in     chan Msg         // receives read messages
	closed <-chan struct{} // receives when peer is shutting down
	wstart <-chan struct{} // receives when write may start
	werr   chan<- error     // for write results
	offset uint64
	w      MsgWriter
}

func (rw *protoRW) ReadMsg() (Msg, error) {
	select {
	case msg := <-rw.in:
		msg.Code -= rw.offset
		return msg, nil
	case <-rw.closed:
		return Msg{}, io.EOF
	}
}

type protoHandshake struct {
	Version    uint64
	Name       string
	Caps       []Cap
	ListenPort uint64
	ID         discover.NodeID

	// Ignore additional fields (for forward compatibility).

	//Rest []rlp.RawValue `rlp:"tail"`
	//Recursive Length Prefix
}

func (p *Peer) Disconnect(reason DiscReason) {
	select {
	case p.disc <- reason:
	case <-p.closed:
	}
}

// matchProtocols creates structures for matching named subprotocols.
func matchProtocols(protocols []Protocol, caps []Cap, rw MsgReadWriter) map[string]*protoRW {
	sort.Sort(capsByNameAndVersion(caps))
	offset := baseProtocolLength
	result := make(map[string]*protoRW)
Outer:
	for _, cap := range caps {
		for _, proto := range protocols {
			if proto.Name == cap.Name && proto.Version == cap.Version {
				// If an old protocol version matched, revert it
				if old := result[cap.Name]; old != nil {
					offset -= old.Length
				}
				// Assign the new match
				result[cap.Name] = &protoRW{Protocol: proto, offset: offset, in: make(chan Msg), w: rw}
				offset += proto.Length
				continue Outer
			}
		}
	}
	return result
}

func newPeer(conn *conn, protocols []Protocol) *Peer {
	protomap := matchProtocols(protocols, conn.caps, conn)
	p := &Peer{
		rw:       conn,
		running:  protomap,
		log:      *log.New(os.Stderr, "", 0), //TODO: 写专门的logger
		created:  mclock.Now(),
		wg:       sync.WaitGroup{},
		protoErr: make(chan error, len(protomap)+1),
		closed:   make(chan struct{}),
		disc:     make(chan DiscReason),
		events:   nil,
	}
	return p
}