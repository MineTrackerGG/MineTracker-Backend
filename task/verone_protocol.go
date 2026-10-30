package task

import (
	"bufio"
	"net"
	"time"
)

// PingJavaConnection reuses the SLP encoder/parser with a caller-validated connection.
func PingJavaConnection(conn net.Conn, virtualHost string, port uint16) (players int, max *int, latency int64, err error) {
	start := time.Now()
	if err = slpSendHandshake(conn, virtualHost, port); err != nil {
		return
	}
	var result *mcPingResult
	result, err = slpReadResponse(bufio.NewReaderSize(conn, 4096))
	if err != nil {
		return
	}
	return result.PlayerCount, result.MaxPlayers, time.Since(start).Milliseconds(), nil
}
