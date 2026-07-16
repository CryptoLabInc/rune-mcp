# consolepb (verbatim copy)

These files are a **verbatim copy** of `CryptoLabInc/rune-console`'s
`pkg/consolepb` — the generated gRPC/protobuf stubs for the console
`ConsoleService`.

rune-mcp keeps its own copy so it does not depend on the `rune-console` module.
It is a pure client of the service, so only the generated code is needed, not
the server.

**Keep in sync manually:** when the console's `console_service.proto` changes,
regenerate on the console side and re-copy both `console_service.pb.go` and
`console_service_grpc.pb.go` here. Do not hand-edit — they are generated code.
