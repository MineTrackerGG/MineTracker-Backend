package verone

import "time"

type Endpoint struct {
	Host string `json:"host" bson:"host"`
	Port uint16 `json:"port" bson:"port"`
	SRV  bool   `json:"srv" bson:"srv"`
}

type Config struct {
	Hosting  bool      `json:"-" bson:"-"`
	ID       string    `json:"id" bson:"_id"`
	Revision int64     `json:"revision" bson:"revision"`
	Enabled  bool      `json:"enabled" bson:"enabled"`
	Deleted  bool      `json:"deleted" bson:"deleted"`
	Interval int       `json:"interval" bson:"interval"`
	Java     *Endpoint `json:"java" bson:"java"`
	Bedrock  *Endpoint `json:"bedrock" bson:"bedrock"`
}

type Reading struct {
	Proxy      bool   `json:"-" bson:"proxy,omitempty"`
	Status     string `json:"status" bson:"status"`
	Players    *int   `json:"players" bson:"players"`
	MaxPlayers *int   `json:"maxPlayers" bson:"maxPlayers"`
	LatencyMS  *int64 `json:"latencyMs" bson:"latencyMs"`
}

type Snapshot struct {
	Proxy      bool      `json:"-" bson:"proxy,omitempty"`
	ID         string    `json:"id" bson:"_id"`
	Revision   int64     `json:"revision" bson:"revision"`
	Status     string    `json:"status" bson:"status"`
	Players    *int      `json:"players" bson:"players"`
	MaxPlayers *int      `json:"maxPlayers" bson:"maxPlayers"`
	LatencyMS  *int64    `json:"latencyMs" bson:"latencyMs"`
	UpdatedAt  time.Time `json:"updatedAt" bson:"updatedAt"`
	Peak       *int      `json:"peak" bson:"peak"`
	Popularity *float64  `json:"popularity" bson:"popularity"`
	Java       *Reading  `json:"java" bson:"java"`
	Bedrock    *Reading  `json:"bedrock" bson:"bedrock"`
}

type Point struct {
	Timestamp  int64 `json:"timestamp"`
	Players    *int  `json:"players"`
	Responding int64 `json:"responding"`
	Samples    int64 `json:"samples"`
}
