package verone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Allocation struct {
	Host string `json:"host" bson:"host"`
	Port uint16 `json:"port" bson:"port"`
}
type InventoryServer struct {
	ID, Name    string
	Suspended   bool
	Allocations []Allocation
}
type InventorySource interface {
	Fetch(context.Context) ([]InventoryServer, error)
}
type Pterodactyl struct {
	base   *url.URL
	token  string
	client *http.Client
}

func NewPterodactyl(rawURL, token string) (*Pterodactyl, error) {
	return newPterodactyl(rawURL, token, net.DefaultResolver)
}
func newPterodactyl(rawURL, token string, dns resolver) (*Pterodactyl, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || token == "" {
		return nil, errors.New("invalid Pterodactyl configuration")
	}
	port := uint16(443)
	if u.Port() != "" {
		p, e := strconv.ParseUint(u.Port(), 10, 16)
		if e != nil {
			return nil, errors.New("invalid panel port")
		}
		port = uint16(p)
	}
	if !ValidEndpoint(&Endpoint{Host: u.Hostname(), Port: port}, false) {
		return nil, errors.New("invalid panel destination")
	}
	transport := &http.Transport{MaxConnsPerHost: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			ip, p, err := resolveWith(ctx, dns, Endpoint{Host: u.Hostname(), Port: port}, false)
			if err != nil {
				return nil, err
			}
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(int(p))))
		}}
	return &Pterodactyl{base: u, token: token, client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("panel redirect blocked") }}}, nil
}

func (p *Pterodactyl) Fetch(ctx context.Context) ([]InventoryServer, error) {
	result := []InventoryServer{}
	seen := map[string]bool{}
	for page := 1; page <= 10; page++ {
		u := *p.base
		u.Path = "/api/application/servers"
		u.RawQuery = fmt.Sprintf("include=allocations&per_page=100&page=%d", page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+p.token)
		req.Header.Set("Accept", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, errors.New("inventory request failed")
		}
		var body struct {
			Data []struct {
				Attributes struct {
					UUID          string  `json:"uuid"`
					Name          string  `json:"name"`
					Suspended     bool    `json:"suspended"`
					Status        *string `json:"status"`
					Relationships struct {
						Allocations *struct {
							Data []struct {
								Attributes struct {
									IP   string `json:"ip"`
									Port uint16 `json:"port"`
								} `json:"attributes"`
							} `json:"data"`
						} `json:"allocations"`
					} `json:"relationships"`
				} `json:"attributes"`
			} `json:"data"`
			Meta struct {
				Pagination struct {
					Current int `json:"current_page"`
					Total   int `json:"total_pages"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
		resp.Body.Close()
		if resp.StatusCode != 200 || readErr != nil || len(data) > 2<<20 || json.Unmarshal(data, &body) != nil {
			return nil, errors.New("invalid inventory response")
		}
		if body.Data == nil {
			return nil, errors.New("missing inventory data")
		}
		pg := body.Meta.Pagination
		if pg.Current != page || pg.Total < page || pg.Total > 10 || len(body.Data) > 100 {
			return nil, errors.New("invalid inventory pagination")
		}
		for _, item := range body.Data {
			a := item.Attributes
			id, err := uuid.Parse(a.UUID)
			if err != nil || id.String() != a.UUID || seen[a.UUID] || len(a.Name) > 512 || (a.Relationships.Allocations == nil || a.Relationships.Allocations.Data == nil) {
				return nil, errors.New("invalid inventory server or missing allocation read permission")
			}
			seen[a.UUID] = true
			row := InventoryServer{ID: a.UUID, Name: a.Name, Suspended: a.Suspended || a.Status != nil, Allocations: []Allocation{}}
			if len(a.Relationships.Allocations.Data) > 64 {
				return nil, errors.New("too many allocations")
			}
			for _, allocation := range a.Relationships.Allocations.Data {
				endpoint := Endpoint{Host: allocation.Attributes.IP, Port: allocation.Attributes.Port}
				if ip, parseErr := netip.ParseAddr(endpoint.Host); parseErr == nil && PublicIP(ip) && ValidEndpoint(&endpoint, false) {
					row.Allocations = append(row.Allocations, Allocation{Host: ip.Unmap().String(), Port: endpoint.Port})
				}
			}
			result = append(result, row)
		}
		if page == pg.Total {
			return result, nil
		}
	}
	return nil, errors.New("inventory exceeds capacity")
}

func panelIdentity(raw string) string { return strings.TrimSuffix(raw, "/") }
