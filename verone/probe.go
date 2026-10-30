package verone

import (
	"MineTracker/task"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)
var blocked = func() []netip.Prefix {
	var result []netip.Prefix
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		result = append(result, netip.MustParsePrefix(s))
	}
	return result
}()

func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.Zone() != "" {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range blocked {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func ValidEndpoint(e *Endpoint, bedrock bool) bool {
	if e == nil {
		return true
	}
	if e.Port == 0 || len(e.Host) > 253 || strings.Contains(e.Host, "%") {
		return false
	}
	if ip, err := netip.ParseAddr(e.Host); err == nil {
		return PublicIP(ip) && !e.SRV
	}
	if !hostPattern.MatchString(e.Host) || !strings.Contains(e.Host, ".") || strings.Contains(e.Host, "..") {
		return false
	}
	for _, label := range strings.Split(e.Host, ".") {
		if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return !e.SRV || (!bedrock && e.Port == 25565)
}

// Resolve every query, validate every answer, and dial the selected literal IP.
// Neither the dialer nor the Minecraft handshake can trigger a second lookup.
type resolver interface {
	LookupSRV(context.Context, string, string, string) (string, []*net.SRV, error)
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func resolve(ctx context.Context, e Endpoint, bedrock bool) (netip.Addr, uint16, error) {
	return resolveWith(ctx, net.DefaultResolver, e, bedrock)
}

func resolveWith(ctx context.Context, dns resolver, e Endpoint, bedrock bool) (netip.Addr, uint16, error) {
	if !ValidEndpoint(&e, bedrock) {
		return netip.Addr{}, 0, errors.New("blocked")
	}
	host, port := e.Host, e.Port
	if e.SRV {
		_, records, err := dns.LookupSRV(ctx, "minecraft", "tcp", host)
		if err == nil && len(records) > 0 {
			if len(records) > 32 {
				return netip.Addr{}, 0, errors.New("too many SRV answers")
			}
			for _, record := range records {
				target := strings.TrimSuffix(record.Target, ".")
				if !ValidEndpoint(&Endpoint{Host: target, Port: record.Port}, false) {
					return netip.Addr{}, 0, errors.New("blocked")
				}
				ips, err := dns.LookupNetIP(ctx, "ip", target)
				if err != nil || len(ips) == 0 {
					return netip.Addr{}, 0, errors.New("dns")
				}
				for _, ip := range ips {
					if !PublicIP(ip) {
						return netip.Addr{}, 0, errors.New("blocked")
					}
				}
				if record == records[0] {
					host, port = ips[0].Unmap().String(), record.Port
				}
			}
		} else if err != nil {
			var dnsErr *net.DNSError
			if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
				return netip.Addr{}, 0, err
			}
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), port, nil
	}
	addresses, err := dns.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return netip.Addr{}, 0, errors.New("dns")
	}
	for _, ip := range addresses {
		if !PublicIP(ip) {
			return netip.Addr{}, 0, errors.New("blocked")
		}
	}
	return addresses[0].Unmap(), port, nil
}

func Probe(ctx context.Context, e Endpoint, bedrock bool) Reading {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ip, port, err := resolve(ctx, e, bedrock)
	if err != nil {
		return Reading{Status: "unknown"}
	}
	network := "tcp"
	if bedrock {
		network = "udp"
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), strconv.Itoa(int(port))))
	if err != nil {
		return Reading{Status: "no_response"}
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	var players int
	var max *int
	var latency int64
	if bedrock {
		players, max, latency, err = pingBedrock(conn)
	} else {
		players, max, latency, err = task.PingJavaConnection(conn, e.Host, port)
	}
	if err != nil {
		return Reading{Status: "no_response"}
	}
	return Reading{Status: "online", Players: &players, MaxPlayers: max, LatencyMS: &latency}
}

var raknetMagic = []byte{0, 255, 255, 0, 254, 254, 254, 254, 253, 253, 253, 253, 18, 52, 86, 120}

func pingBedrock(conn net.Conn) (int, *int, int64, error) {
	start := time.Now()
	packet := make([]byte, 33)
	packet[0] = 1
	_, _ = rand.Read(packet[1:9])
	copy(packet[9:25], raknetMagic)
	_, _ = rand.Read(packet[25:33])
	if _, err := conn.Write(packet); err != nil {
		return 0, nil, 0, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, nil, 0, err
	}
	if n < 35 || buf[0] != 0x1c || !bytes.Equal(buf[1:9], packet[1:9]) || !bytes.Equal(buf[17:33], raknetMagic) {
		return 0, nil, 0, errors.New("invalid pong")
	}
	length := int(binary.BigEndian.Uint16(buf[33:35]))
	if length > n-35 {
		return 0, nil, 0, io.ErrUnexpectedEOF
	}
	parts := strings.Split(string(buf[35:35+length]), ";")
	if len(parts) < 6 || (parts[0] != "MCPE" && parts[0] != "MCEE") {
		return 0, nil, 0, errors.New("invalid advertisement")
	}
	players, err := strconv.Atoi(parts[4])
	if err != nil || players < 0 || players > 1000000 {
		return 0, nil, 0, errors.New("invalid players")
	}
	max, err := strconv.Atoi(parts[5])
	if err != nil || max < 0 || max > 1000000 {
		return 0, nil, 0, errors.New("invalid slots")
	}
	return players, &max, time.Since(start).Milliseconds(), nil
}
