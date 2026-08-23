module github.com/Print-Pilot/printpilot-agent

go 1.25.2

require (
	github.com/Print-Pilot/printpilot-protocol v0.0.0-00010101000000-000000000000
	github.com/coder/websocket v1.8.15
	gopkg.in/yaml.v3 v3.0.1
)

require gopkg.in/natefinch/lumberjack.v2 v2.2.1

replace github.com/Print-Pilot/printpilot-protocol => ../printpilot-protocol
