package data

import (
	"encoding/json"
	"os"
)

type PingableServer struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	Type string `json:"type"`
}

type Server struct {
	Name        string `json:"name"`
	IP          string `json:"ip"`
	Icon        string `json:"icon,omitempty"`
	Type        string `json:"type"`
	Online      bool   `json:"online"`
	PlayerCount int    `json:"player_count"`
	Peak        int    `json:"peak"`
}

type ExtendedServer struct {
	PingableServer
	Name    string `json:"name"`
	Icon    string `json:"icon,omitempty"`
	Online  bool   `json:"online"`
	Current int    `json:"current_players"`
	Peak    int    `json:"peak_players"`
	Mean    int    `json:"mean_players"`
	Lowest  int    `json:"lowest_players"`
}

func LoadServers(path string) ([]PingableServer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var servers []PingableServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return nil, err
	}

	return servers, nil
}
