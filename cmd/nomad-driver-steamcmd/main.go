package main

import (
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/nomad/plugins"

	steamcmd "github.com/byteford/nomad-driver-steamcmd/driver"
)

func main() {
	plugins.Serve(factory)
}

func factory(log hclog.Logger) interface{} {
	return steamcmd.NewPlugin(log)
}
