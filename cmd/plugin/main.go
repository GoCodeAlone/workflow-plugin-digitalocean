// Command workflow-plugin-digitalocean is a workflow engine external plugin that
// provides DigitalOcean infrastructure provisioning via the IaC provider interface.
// It runs as a subprocess and communicates with the host via the go-plugin protocol.
package main

import (
	"github.com/GoCodeAlone/workflow-plugin-digitalocean/internal"
	sdk "github.com/GoCodeAlone/workflow/plugin/external/sdk"
)

func main() {
	sdk.Serve(internal.NewDOPlugin())
}
