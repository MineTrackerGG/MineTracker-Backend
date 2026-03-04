package task

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mcPingResult holds only the fields we use from a Minecraft SLP response.
type mcPingResult struct {
	PlayerCount int
	Favicon     string
}

// serverPinger is satisfied by pooledPinger (and by test fakes).
type serverPinger interface {
	ping(host string, port uint16, timeout time.Duration) (*mcPingResult, error)
}

// pooledPinger performs Minecraft Server List Ping (SLP) using a sync.Pool of
// bufio.Readers. The pool reuses the 4KB internal read buffer instead of
// allocating a fresh one on every call — which was the dominant source of GC
// pressure in the previous go-mcping-based implementation.
type pooledPinger struct {
	pool sync.Pool
}

func newPooledPinger() *pooledPinger {
	return &pooledPinger{
		pool: sync.Pool{
			New: func() interface{} {
				return bufio.NewReaderSize(nil, 4096)
			},
		},
	}
}

func (p *pooledPinger) ping(host string, port uint16, timeout time.Duration) (*mcPingResult, error) {
	// SRV resolution: _minecraft._tcp.<host>
	// Matches go-mcping behaviour; fails quickly (NXDOMAIN) for plain IPs.
	if _, srvs, err := net.LookupSRV("minecraft", "tcp", host); err == nil && len(srvs) > 0 {
		host = strings.TrimSuffix(srvs[0].Target, ".")
		port = srvs[0].Port
	}

	conn, err := net.DialTimeout("tcp", host+":"+strconv.FormatUint(uint64(port), 10), timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout)) //nolint:errcheck

	if err := slpSendHandshake(conn, host, port); err != nil {
		return nil, err
	}

	br := p.pool.Get().(*bufio.Reader)
	br.Reset(conn)
	// pool.Put happens after slpReadResponse returns so that the zero-copy
	// Peek path inside slpReadResponse can safely borrow br's internal buffer.
	result, err := slpReadResponse(br)
	p.pool.Put(br)
	return result, err
}

// slpSendHandshake writes the SLP handshake + status-request packets in a
// single conn.Write to avoid two separate syscalls.
func slpSendHandshake(conn net.Conn, host string, port uint16) error {
	// Maximum: 1(len) + 1(ID) + 5(proto) + 1(hostLen) + 255(host) + 2(port) + 1(state) + 2(req)
	var buf [512]byte
	n := 0

	// Build the packet body in a temporary buffer, then prefix with its varint length.
	var body [300]byte
	bn := 0
	bn += putMCVarint(body[bn:], 0x00) // packet ID: handshake
	bn += putMCVarint(body[bn:], 47)   // protocol 47 (1.8) — accepted by all modern servers
	bn += putMCString(body[bn:], host) // server address
	body[bn], body[bn+1] = byte(port>>8), byte(port)
	bn += 2                         // server port (big-endian uint16)
	bn += putMCVarint(body[bn:], 1) // next state: status

	n += putMCVarint(buf[n:], uint32(bn))
	copy(buf[n:], body[:bn])
	n += bn

	// Status-request packet: [length=1][packet ID=0x00]
	buf[n] = 0x01
	buf[n+1] = 0x00
	n += 2

	_, err := conn.Write(buf[:n])
	return err
}

// slpReadResponse reads and parses the SLP status-response packet.
func slpReadResponse(r *bufio.Reader) (*mcPingResult, error) {
	// Packet length varint — discard, bufio handles framing for us.
	if _, err := binary.ReadUvarint(r); err != nil {
		return nil, err
	}

	// Packet ID must be 0x00.
	id, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if id != 0x00 {
		return nil, errors.New("mcping: unexpected packet ID")
	}

	// JSON payload length.
	jsonLen, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	if jsonLen < 2 || jsonLen > 1<<20 {
		return nil, errors.New("mcping: invalid JSON length")
	}

	// Read JSON bytes.
	// Fast path: if the entire payload is already in the bufio buffer (common
	// for responses ≤ 4KB), Peek returns a zero-copy slice from the internal
	// buffer — no heap allocation needed for the JSON bytes.
	n := int(jsonLen)
	var jsonBytes []byte
	if r.Buffered() >= n {
		jsonBytes, _ = r.Peek(n)
		r.Discard(n) //nolint:errcheck
	} else {
		jsonBytes = make([]byte, n)
		if _, err = io.ReadFull(r, jsonBytes); err != nil {
			return nil, err
		}
	}

	// Unmarshal directly into a typed struct.
	// The old go-mcping used json.NewDecoder → map[string]interface{} → jsonq,
	// which boxed every JSON value as interface{} and allocated heavily.
	var resp struct {
		Players struct {
			Online int `json:"online"`
		} `json:"players"`
		Favicon string `json:"favicon"`
	}
	if err := json.Unmarshal(jsonBytes, &resp); err != nil {
		return nil, err
	}
	return &mcPingResult{PlayerCount: resp.Players.Online, Favicon: resp.Favicon}, nil
}

// ---- Minecraft varint / string encoding helpers ----------------------------

// putMCVarint encodes v as a Minecraft Protocol VarInt into buf and returns
// the number of bytes written (1–5).
func putMCVarint(buf []byte, v uint32) int {
	n := 0
	for v >= 0x80 {
		buf[n] = byte(v&0x7F) | 0x80
		v >>= 7
		n++
	}
	buf[n] = byte(v)
	return n + 1
}

// putMCString encodes s as a Minecraft Protocol String
// (VarInt byte length followed by UTF-8 bytes).
func putMCString(buf []byte, s string) int {
	n := putMCVarint(buf, uint32(len(s)))
	copy(buf[n:], s)
	return n + len(s)
}
