module github.com/corescope/meshfeed-publisher

go 1.26.0

require (
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/meshcore-analyzer/packetpath v0.0.0
	github.com/meshcore-analyzer/sigvalidate v0.0.0
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	golang.org/x/net v0.44.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
)

replace github.com/meshcore-analyzer/packetpath => ../../internal/packetpath

replace github.com/meshcore-analyzer/sigvalidate => ../../internal/sigvalidate
