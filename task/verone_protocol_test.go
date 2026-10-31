package task

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

func TestSLPRejectsMalformedFrames(t *testing.T) {
	for _, payload := range []string{`{}`, `{"players":{"online":null}}`, `{"players":{"online":-1}}`, `{"players":{"online":1,"max":-1}}`, `{"players":{"online":1000001}}`} {
		data := []byte{0}
		data = binary.AppendUvarint(data, uint64(len(payload)))
		data = append(data, []byte(payload)...)
		data = append(binary.AppendUvarint(nil, uint64(len(data))), data...)
		if _, err := slpReadResponse(bufio.NewReader(bytes.NewReader(data))); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	if _, err := slpReadResponse(bufio.NewReader(bytes.NewReader([]byte{5, 0, 2, '{', '}', 0}))); err == nil {
		t.Fatal("accepted mismatched frame length")
	}
}

func TestVeronePrivateProxyHint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		proxy bool
	}{{"Velocity 3.4", true}, {"BungeeCord 1.21", true}, {"Waterfall", true}, {"Geyser Standalone", true}, {"Paper 1.21", false}, {"Paper 1.21 + Geyser plugin", false}, {"Fabric 1.21", false}} {
		payload := []byte(`{"version":{"name":"` + tc.name + `"},"players":{"online":8,"max":100}}`)
		result, err := slpReadResponse(bufio.NewReader(bytes.NewReader(buildSLPPacket(0, payload))))
		if err != nil || result.Proxy != tc.proxy || result.PlayerCount != 8 {
			t.Fatalf("%s %+v %v", tc.name, result, err)
		}
	}
}
