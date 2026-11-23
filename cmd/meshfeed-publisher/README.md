# meshfeed-publisher

Derived-feed publisher for the meshfeed MQTT aggregator (feed.sacmesh.org).
It subscribes to meshfeed's raw stream (`meshcore/+/+/packets`, `meshcore/+/+/status`)
and publishes:

| Topic | Retained | Content |
|---|---|---|
| `meshcore-decoded/<IATA>/<observer>/packets` | no | One message per observation: hash, SNR/RSSI, raw hex and the full CoreScope decode (`header`, `path`, `payload`), channel text decrypted where a key is known. |
| `meshcore-merged/<PAYLOAD_TYPE>` | no | One message per unique packet after an 8 s window, listing every observer, region, SNR/RSSI and hop list. |
| `meshcore-nodes/<PUBKEY>` | yes | Node directory from signature-valid adverts (name, role, position, last advert). Cleared after 14 days without an advert. |
| `meshcore-observers/<IATA>/<observer>` | yes | Observer roster: status/hardware fields, last status/packet times. Cleared after 7 days of silence. |
| `meshfeed-health/publisher` | yes | Counters and uptime every 30 s; `{"status":"offline"}` as last will. |

`decoder.go` and `util.go` are copies of `cmd/ingestor` (a `main` package cannot be
imported). Re-copy them when the ingestor decoder changes so decodes match CoreScope.

Config (`-config`, JSON): `broker` (default `tcp://broker:1883`), `usernameEnv`,
`passwordEnv`, `clientId`, `channelKeys` (name → 32 hex), `hashChannels`,
`mergeWindowSeconds`, `seedSeconds`. On start it reads its own retained node and
observer messages back for `seedSeconds` so a restart does not forget or re-announce them.

Build: `CGO_ENABLED=0 go build -trimpath -o meshfeed-publisher .` (pure Go), or
`make build-meshfeed-publisher`.
