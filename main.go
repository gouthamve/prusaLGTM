package main

import (
	"github.com/alecthomas/kong"
	"github.com/gouthamve/prusaLGTM/cli"
)

func main() {
	ctx := kong.Parse(&cli.PrusaLGTM, kong.Name("prusaLGTM"),
		kong.Description("Monitor Prusa using Loki and Prometheus to make sure it is looking good."),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
			Summary: true,
		}))

	ctx.FatalIfErrorf(ctx.Run())
}
