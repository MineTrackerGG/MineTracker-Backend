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
