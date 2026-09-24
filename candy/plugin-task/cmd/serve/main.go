package main

import (
	plugintask "github.com/opencharly/plugin-task/candy/plugin-task"
	"github.com/opencharly/sdk"
)

func main() {
	sdk.Main(plugintask.NewProvider(), plugintask.NewMeta(), plugintask.CliMain)
}
