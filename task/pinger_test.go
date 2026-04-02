package task

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

func encodeVarint(v uint64) []byte {
	b := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(b, v)
	return b[:n]
}

func buildSLPPacket(packetID byte, jsonBody []byte) []byte {
	jsonLen := encodeVarint(uint64(len(jsonBody)))
	payload := make([]byte, 0, 1+len(jsonLen)+len(jsonBody))
	payload = append(payload, packetID)
	payload = append(payload, jsonLen...)
	payload = append(payload, jsonBody...)

	packetLen := encodeVarint(uint64(len(payload)))
	packet := make([]byte, 0, len(packetLen)+len(payload))
	packet = append(packet, packetLen...)
	packet = append(packet, payload...)
	return packet
}

func TestPutMCVarint(t *testing.T) {
	buf := make([]byte, 5)
	n := putMCVarint(buf, 300)
	if n != 2 {
		t.Fatalf("expected 2 bytes for 300, got %d", n)
	}
	if buf[0] != 0xAC || buf[1] != 0x02 {
		t.Fatalf("unexpected varint bytes: %x %x", buf[0], buf[1])
	}
}

func TestPutMCString(t *testing.T) {
	buf := make([]byte, 32)
	n := putMCString(buf, "abc")
	if n != 4 {
		t.Fatalf("expected encoded length 4, got %d", n)
	}
	if buf[0] != 0x03 {
		t.Fatalf("expected string length prefix 0x03, got 0x%x", buf[0])
	}
	if string(buf[1:4]) != "abc" {
		t.Fatalf("unexpected string payload: %q", string(buf[1:4]))
	}
}

func TestSLPReadResponseSuccess(t *testing.T) {
	jsonBody := []byte(`{"players":{"online":42},"favicon":"data:image/png;base64,AAA"}`)
	packet := buildSLPPacket(0x00, jsonBody)

	r := bufio.NewReader(bytes.NewReader(packet))
	resp, err := slpReadResponse(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.PlayerCount != 42 {
		t.Fatalf("expected player count 42, got %d", resp.PlayerCount)
	}
	if resp.Favicon == "" {
		t.Fatal("expected favicon to be populated")
	}
}

func TestSLPReadResponseUnexpectedPacketID(t *testing.T) {
	jsonBody := []byte(`{"players":{"online":1}}`)
	packet := buildSLPPacket(0x01, jsonBody)

	r := bufio.NewReader(bytes.NewReader(packet))
	_, err := slpReadResponse(r)
	if err == nil {
		t.Fatal("expected error for unexpected packet id")
	}
}

func TestSLPReadResponseInvalidJSONLength(t *testing.T) {
	packetLen := encodeVarint(2)
	packet := append(packetLen, 0x00)
	packet = append(packet, encodeVarint((1<<20)+1)...)

	r := bufio.NewReader(bytes.NewReader(packet))
	_, err := slpReadResponse(r)
	if err == nil {
		t.Fatal("expected error for invalid json length")
	}
}

func TestResolveTargetSkipsDNSForIP(t *testing.T) {
	p := newPooledPinger()
	host, port := p.resolveTarget("127.0.0.1", 25565)

	if host != "127.0.0.1" {
		t.Fatalf("expected same host, got %s", host)
	}
	if port != 25565 {
		t.Fatalf("expected same port, got %d", port)
	}

	if _, ok := p.srvCache.Load("127.0.0.1"); ok {
		t.Fatal("expected no cache entry for raw ip host")
	}
}
