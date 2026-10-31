package verone

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Only these derived labels and a technical fingerprint survive the API response.
type Discovery struct {
	Kind      string `json:"kind" bson:"kind"`
	Uncertain bool   `json:"uncertain" bson:"uncertain"`
	Signature string `json:"-" bson:"signature"`
}
type panelContainer struct {
	Startup     string         `json:"startup_command"`
	Environment map[string]any `json:"environment"`
}

var javaLaunch = regexp.MustCompile(`(?i)(?:^|[;&|])\s*(?:exec\s+)?(?:[^\s;&|]+/)?java(?:\s|$)`)
var bedrockLaunch = regexp.MustCompile(`(?i)(?:^|[;&|])\s*(?:exec\s+)?(?:[^\s;&|]+/)?bedrock_server(?:\s|$)`)
var otherLaunch = regexp.MustCompile(`(?i)(?:^|[;&|])\s*(?:exec\s+)?(?:[^\s;&|]+/)?(?:node|python3?|TerrariaServer\.bin\.x86_64|RustDedicated|srcds_run)(?:\s|$)`)
var launchedJar = regexp.MustCompile(`(?i)(?:^|\s)-jar\s+(?:"([^"]+)"|'([^']+)'|([^\s;&|]+))`)
var proxyArtifact = regexp.MustCompile(`(?i)^(velocity|bungee(?:cord)?|waterfall|geyser(?:[-_]standalone)?|mcxboxbroadcaststandalone|viaproxy)(?:[-_.0-9]|$)`)
var backendArtifact = regexp.MustCompile(`(?i)^(paper|purpur|spigot|fabric-server-launch|neoforge|forge|vanilla)(?:[-_.0-9]|$)`)
var technicalVersion = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,64}$`)

func envText(env map[string]any, key string) string {
	value := ""
	switch raw := env[key].(type) {
	case string:
		value = raw
	case float64:
		value = strconv.FormatFloat(raw, 'f', -1, 64)
	case bool:
		value = strconv.FormatBool(raw)
	}
	if len(value) > 512 {
		return ""
	}
	return strings.TrimSpace(value)
}
func proxyEnvironment(env map[string]any, egg string) bool {
	for _, key := range []string{"VELOCITY_VERSION", "BUNGEE_VERSION", "BUNGEECORD_VERSION", "WATERFALL_VERSION", "GEYSER_STANDALONE_VERSION", "VIAPROXY_VERSION", "MCXBOXBROADCAST_VERSION"} {
		if envText(env, key) != "" {
			return true
		}
	}
	label := strings.ToLower(egg)
	return strings.Contains(label, "geyser") && strings.Contains(label, "standalone") && envText(env, "GEYSER_VERSION") != ""
}
func discover(container panelContainer, egg string) Discovery {
	startup := container.Startup
	if len(startup) > 8192 {
		return Discovery{Kind: "unknown", Uncertain: true, Signature: "oversized"}
	}
	java, bedrock := javaLaunch.MatchString(startup), bedrockLaunch.MatchString(startup)
	artifact := ""
	if java {
		if match := launchedJar.FindStringSubmatch(startup); match != nil {
			for _, part := range match[1:] {
				if part != "" {
					artifact = part
					break
				}
			}
			if artifact == "{{SERVER_JARFILE}}" || artifact == "${SERVER_JARFILE}" || artifact == "$SERVER_JARFILE" {
				artifact = envText(container.Environment, "SERVER_JARFILE")
			}
			if artifact != "" {
				artifact = strings.ToLower(path.Base(artifact))
			}
		}
	}
	project := ""
	if u, err := url.Parse(envText(container.Environment, "DL_PATH")); err == nil && u.Scheme == "https" && (u.Hostname() == "api.papermc.io" || u.Hostname() == "fill.papermc.io" || u.Hostname() == "api.purpurmc.org") {
		for _, part := range strings.Split(u.Path, "/") {
			switch part {
			case "velocity", "waterfall", "paper", "purpur":
				project = part
			}
		}
	}
	kind := "unknown"
	switch {
	case java && artifact != "" && (proxyArtifact.MatchString(artifact) || project == "velocity" || project == "waterfall"):
		kind = "proxy"
	case java && artifact != "" && !backendArtifact.MatchString(artifact) && proxyEnvironment(container.Environment, egg):
		kind = "proxy"
	case java:
		kind = "java"
	case bedrock || envText(container.Environment, "BEDROCK_VERSION") != "":
		kind = "bedrock"
	case otherLaunch.MatchString(startup):
		kind = "other"
	}
	// A residual download variable must not override an explicitly recognized backend JAR.
	if java && backendArtifact.MatchString(artifact) {
		kind = "java"
	}
	fingerprint := kind + ":" + artifact + ":" + project
	for _, key := range []string{"MINECRAFT_VERSION", "MC_VERSION", "VANILLA_VERSION", "FORGE_VERSION", "FABRIC_VERSION", "NEOFORGE_VERSION", "BEDROCK_VERSION", "VELOCITY_VERSION", "BUNGEE_VERSION", "BUNGEECORD_VERSION", "WATERFALL_VERSION", "GEYSER_STANDALONE_VERSION", "GEYSER_VERSION", "VIAPROXY_VERSION", "MCXBOXBROADCAST_VERSION"} {
		value := envText(container.Environment, key)
		if technicalVersion.MatchString(value) {
			fingerprint += "|" + key + "=" + value
		}
	}
	sum := sha256.Sum256([]byte(fingerprint))
	knownBackend := backendArtifact.MatchString(artifact)
	for _, key := range []string{"FORGE_VERSION", "FABRIC_VERSION", "NEOFORGE_VERSION", "VANILLA_VERSION"} {
		if envText(container.Environment, key) != "" {
			knownBackend = true
		}
	}
	uncertain := kind == "unknown" || kind == "java" && !knownBackend || kind == "bedrock" && !bedrock
	return Discovery{Kind: kind, Uncertain: uncertain, Signature: hex.EncodeToString(sum[:])}
}
func automaticParticipation(item InventoryServer, revision int64) Participation {
	p := Participation{Revision: revision, Role: "standalone", Mode: "backends", Enabled: true}
	if item.Discovery.Kind == "proxy" || item.Discovery.Kind == "other" {
		p.Role = "excluded"
		return p
	}
	if item.Primary == nil {
		return p
	}
	e := &Endpoint{Host: canonicalHostingHost(item.Primary.Host), Port: item.Primary.Port}
	// Recheck at the use boundary, including sources supplied by another adapter.
	allowed := false
	for _, a := range item.Allocations {
		if a.Host == e.Host && a.Port == e.Port {
			allowed = true
		}
	}
	ip, err := netip.ParseAddr(e.Host)
	if !allowed || err != nil || !PublicIP(ip) || !ValidEndpoint(e, false) {
		return p
	}
	switch item.Discovery.Kind {
	case "java":
		p.Query.Java = e
	case "bedrock":
		p.Query.Bedrock = e
	default:
		p.Query.Java = e
		copy := *e
		p.Query.Bedrock = &copy
	}
	return p
}
