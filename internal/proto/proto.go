// Package proto defines the wire format shared by mi-client and mi-server.
//
// Every UDP datagram is:
//
//	header (20 bytes, cleartext, authenticated as AAD) || XChaCha20-Poly1305 ciphertext
//
// Header layout:
//
//	[0]     version
//	[1]     message type
//	[2]     path ID (which physical link the sender used)
//	[3]     direction (0 = client→server, 1 = server→client)
//	[4:12]  session ID (chosen by the client at start-up, strictly increasing)
//	[12:20] counter (per-sender, unique for every datagram; used as nonce + anti-replay)
//
// Speed-test payloads (all big-endian, inside the sealed body):
//
//	SPEED_REQ  [0:4] test ID | [4] op | [5:9] duration in ms | [9:11] rate cap in Mbit/s (0 = default)   (11 bytes)
//	SPEED_FILL [0:4] test ID | zero padding up to the requested size (only the ID is read)
//	SPEED_RES  [0:4] test ID | [4:12] bytes received | [12:20] span in ns (last - first arrival)         (20 bytes)
package proto

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	Version   = 1
	HeaderLen = 20
	KeyLen    = chacha20poly1305.KeySize
	// Overhead is the number of bytes a datagram adds on top of its plaintext payload.
	Overhead = HeaderLen + chacha20poly1305.Overhead
	// DataOverhead is the total tunnel overhead for one IP packet (outer IP/UDP excluded).
	DataOverhead = Overhead + dataSeqLen
)

type Type byte

const (
	TypeData     Type = 1
	TypeProbe    Type = 2
	TypeProbeAck Type = 3
	// Speed test (see the package comment for payloads).
	TypeSpeedReq  Type = 4 // client -> server: start/query a speed test on the link it arrives on
	TypeSpeedFill Type = 5 // filler datagrams, either direction, counted by the receiver
	TypeSpeedRes  Type = 6 // server -> client: result of an upload test
)

type Dir byte

const (
	ClientToServer Dir = 0
	ServerToClient Dir = 1
)

type Header struct {
	Type    Type
	PathID  uint8
	Dir     Dir
	Session uint64
	Counter uint64
}

var (
	ErrShort   = errors.New("proto: packet too short")
	ErrVersion = errors.New("proto: unknown version")
	ErrKeyLen  = errors.New("proto: key must be 32 bytes")
)

func (h Header) put(b []byte) {
	b[0] = Version
	b[1] = byte(h.Type)
	b[2] = h.PathID
	b[3] = byte(h.Dir)
	binary.BigEndian.PutUint64(b[4:12], h.Session)
	binary.BigEndian.PutUint64(b[12:20], h.Counter)
}

func parseHeader(b []byte) (Header, error) {
	if len(b) < Overhead {
		return Header{}, ErrShort
	}
	if b[0] != Version {
		return Header{}, ErrVersion
	}
	return Header{
		Type:    Type(b[1]),
		PathID:  b[2],
		Dir:     Dir(b[3]),
		Session: binary.BigEndian.Uint64(b[4:12]),
		Counter: binary.BigEndian.Uint64(b[12:20]),
	}, nil
}

// Cipher seals and opens datagrams with a pre-shared 32-byte key.
type Cipher struct{ aead cipher.AEAD }

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeyLen {
		return nil, ErrKeyLen
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// nonce = session(8) || dir(1) || zero(7) || counter(8). Session is unique per client
// start, direction separates the two senders, counter is unique per sender.
func nonce(h Header) [chacha20poly1305.NonceSizeX]byte {
	var n [chacha20poly1305.NonceSizeX]byte
	binary.BigEndian.PutUint64(n[0:8], h.Session)
	n[8] = byte(h.Dir)
	binary.BigEndian.PutUint64(n[16:24], h.Counter)
	return n
}

// Seal appends header||ciphertext to dst and returns the result.
func (c *Cipher) Seal(dst []byte, h Header, plaintext []byte) []byte {
	start := len(dst)
	dst = append(dst, make([]byte, HeaderLen)...)
	h.put(dst[start:])
	n := nonce(h)
	return c.aead.Seal(dst, n[:], plaintext, dst[start:start+HeaderLen])
}

// Open authenticates and decrypts a datagram, appending the plaintext to dst.
func (c *Cipher) Open(dst []byte, packet []byte) (Header, []byte, error) {
	h, err := parseHeader(packet)
	if err != nil {
		return Header{}, nil, err
	}
	n := nonce(h)
	pt, err := c.aead.Open(dst, n[:], packet[HeaderLen:], packet[:HeaderLen])
	if err != nil {
		return Header{}, nil, err
	}
	return h, pt, nil
}

const dataSeqLen = 8

// PutData builds a DATA payload: data sequence number || IP packet.
// The data sequence is separate from the header counter so that probes and
// redundant copies never create gaps in the reorder buffer.
func PutData(seq uint64, pkt []byte) []byte {
	b := make([]byte, dataSeqLen+len(pkt))
	binary.BigEndian.PutUint64(b, seq)
	copy(b[dataSeqLen:], pkt)
	return b
}

func ParseData(b []byte) (seq uint64, pkt []byte, err error) {
	if len(b) < dataSeqLen {
		return 0, nil, ErrShort
	}
	return binary.BigEndian.Uint64(b), b[dataSeqLen:], nil
}

const probeLen = 16

// PutProbe builds a PROBE / PROBE_ACK payload: send time (unix nanos) ||
// the bandwidth (kbit/s) the server should assume when sending down this path ||
// the speed cap (kbit/s, 0 = none) the server must enforce on that path's
// download. The cap travels in every probe so it survives packet loss and a
// server restart without any extra control message.
func PutProbe(sentUnixNano int64, downKbps, downLimitKbps uint32) []byte {
	b := make([]byte, probeLen)
	binary.BigEndian.PutUint64(b, uint64(sentUnixNano))
	binary.BigEndian.PutUint32(b[8:], downKbps)
	binary.BigEndian.PutUint32(b[12:], downLimitKbps)
	return b
}

func ParseProbe(b []byte) (sentUnixNano int64, downKbps, downLimitKbps uint32, err error) {
	if len(b) < probeLen {
		return 0, 0, 0, ErrShort
	}
	return int64(binary.BigEndian.Uint64(b)), binary.BigEndian.Uint32(b[8:]), binary.BigEndian.Uint32(b[12:]), nil
}

// PutProbeAck echoes a probe payload and appends the server's boot ID, which
// lets the client notice a server restart and start a fresh session.
func PutProbeAck(probe []byte, boot uint64) []byte {
	b := make([]byte, probeLen+8)
	copy(b, probe[:probeLen])
	binary.BigEndian.PutUint64(b[probeLen:], boot)
	return b
}

func ParseProbeAck(b []byte) (sentUnixNano int64, boot uint64, err error) {
	if len(b) < probeLen+8 {
		return 0, 0, ErrShort
	}
	return int64(binary.BigEndian.Uint64(b)), binary.BigEndian.Uint64(b[probeLen:]), nil
}

// SpeedOp says what a SPEED_REQ asks the server to do.
type SpeedOp uint8

const (
	SpeedDown     SpeedOp = 1 // flood this link with SPEED_FILL (download test)
	SpeedUpResult SpeedOp = 2 // report what arrived for the test ID (upload test)
)

const (
	speedReqLen  = 11
	speedFillMin = 4
	speedResLen  = 20
)

// PutSpeedReq builds a SPEED_REQ. The duration travels in whole milliseconds
// (saturating) and the rate cap in Mbit/s.
func PutSpeedReq(testID uint32, op SpeedOp, dur time.Duration, rateCapMbps uint16) []byte {
	ms := dur.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	if ms > 1<<32-1 {
		ms = 1<<32 - 1
	}
	b := make([]byte, speedReqLen)
	binary.BigEndian.PutUint32(b, testID)
	b[4] = byte(op)
	binary.BigEndian.PutUint32(b[5:], uint32(ms))
	binary.BigEndian.PutUint16(b[9:], rateCapMbps)
	return b
}

func ParseSpeedReq(b []byte) (testID uint32, op SpeedOp, dur time.Duration, rateCapMbps uint16, err error) {
	if len(b) < speedReqLen {
		return 0, 0, 0, 0, ErrShort
	}
	return binary.BigEndian.Uint32(b), SpeedOp(b[4]),
		time.Duration(binary.BigEndian.Uint32(b[5:])) * time.Millisecond, binary.BigEndian.Uint16(b[9:]), nil
}

// PutSpeedFill builds a SPEED_FILL payload of exactly size bytes (at least 4).
func PutSpeedFill(testID uint32, size int) []byte {
	if size < speedFillMin {
		size = speedFillMin
	}
	b := make([]byte, size)
	binary.BigEndian.PutUint32(b, testID)
	return b
}

func ParseSpeedFill(b []byte) (testID uint32, err error) {
	if len(b) < speedFillMin {
		return 0, ErrShort
	}
	return binary.BigEndian.Uint32(b), nil
}

// PutSpeedRes builds a SPEED_RES: what the server received for testID and over
// which span (last minus first arrival).
func PutSpeedRes(testID uint32, bytes uint64, span time.Duration) []byte {
	b := make([]byte, speedResLen)
	binary.BigEndian.PutUint32(b, testID)
	binary.BigEndian.PutUint64(b[4:], bytes)
	binary.BigEndian.PutUint64(b[12:], uint64(span))
	return b
}

func ParseSpeedRes(b []byte) (testID uint32, bytes uint64, span time.Duration, err error) {
	if len(b) < speedResLen {
		return 0, 0, 0, ErrShort
	}
	return binary.BigEndian.Uint32(b), binary.BigEndian.Uint64(b[4:]), time.Duration(binary.BigEndian.Uint64(b[12:])), nil
}
