# MineTracker Backend for Verone

The Verone monitor runs with `go run ./cmd/verone` or the image defined in `deploy/Dockerfile`. Configure distinct server-side read/admin tokens, MongoDB and InfluxDB using `deploy/.env.example`.

See [setup, API, limits and recovery](docs/verone.md) before deploying. The deployment supports one monitor replica per database. The original upstream `go run .` executable remains separate.

Run `go test ./...` and `go vet ./...` for local checks. The optional database integration test requires `VERONE_TEST_MONGO_URI`, `VERONE_TEST_INFLUX_URL`, `INFLUXDB_ORG`, `INFLUXDB_BUCKET` and `INFLUXDB_TOKEN`, pointing exclusively to disposable databases. It drops the `verone_minecraft_test` Mongo database and writes samples to the supplied test bucket. `VERONE_TEST_LIVE=1 go test -v ./verone -run TestLivePublicProbes -count=1` performs one public Java and one public Bedrock query; these checks depend on external network/server availability.

Optional [read-only Pterodactyl hosting inventory and aggregate](docs/hosting.md) is separate from the editorial catalog.
