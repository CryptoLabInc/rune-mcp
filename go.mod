module github.com/CryptoLabInc/rune-mcp

go 1.26.4

require (
	github.com/CryptoLabInc/rune-console v0.0.0-20260506055025-ad52b6bd549d
	github.com/CryptoLabInc/runed v0.1.0
	github.com/CryptoLabInc/runespace-sdk v0.1.3
	github.com/google/uuid v1.6.0
	github.com/modelcontextprotocol/go-sdk v1.5.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171
	google.golang.org/grpc v1.81.0
)

// Local siblings (integration test): runeconsole provides the ConsoleService stubs;
// the runespace SDK provides client-side EncKey encryption (cgo).
replace github.com/CryptoLabInc/rune-console => ../rune-console

replace github.com/CryptoLabInc/runespace-sdk => ../runespace-sdk

replace github.com/CryptoLabInc/runed => ../runed

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.11-20260415201107-50325440f8f2.1 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
