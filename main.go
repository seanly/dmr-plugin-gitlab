// dmr-plugin-gitlab is an external DMR plugin that receives GitLab MR webhooks
// and triggers code review via the DMR agent loop.
// It runs as a separate process, communicating with DMR via HashiCorp go-plugin (net/rpc).
package main

import (
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/seanly/dmr/pkg/plugin/proto"
)

func main() {
	impl := NewGitLabPlugin()

	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: proto.Handshake,
		Plugins: map[string]goplugin.Plugin{
			"dmr-plugin": &proto.DMRPlugin{Impl: impl},
		},
	})
}
