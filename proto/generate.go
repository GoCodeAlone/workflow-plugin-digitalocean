// Package proto contains the generated gRPC/protobuf bindings for the
// DigitalOcean workflow plugin.
//
// The checked-in digitalocean.pb.go was generated with:
//
//	protoc v25.3 (libprotoc 25.3)
//	protoc-gen-go v1.34.2 (google.golang.org/protobuf)
//
// To regenerate after editing digitalocean.proto, install the same tool
// versions and run:
//
//	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
//	go generate ./proto/
//
// Regenerating with different tool versions may produce cosmetic diffs in
// the output; keep the checked-in version stable to avoid noise in code
// review.
package proto

//go:generate sh -c "cd .. && protoc --go_out=. --go_opt=paths=source_relative proto/digitalocean.proto"
