package main

import (
	"fmt"
	"net/http"

	"github.com/alecthomas/kong"
	"github.com/gouthamve/prusaLGTM/cli"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	ctx := kong.Parse(&cli.PrusaLGTM, kong.Name("prusaLGTM"),
		kong.Description("Monitor Prusa using Loki and Prometheus to make sure it is looking good."),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
			Summary: true,
		}))

	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(fmt.Sprintf(":%d", cli.PrusaLGTM.PrometheusPort), nil)

	ctx.FatalIfErrorf(ctx.Run())
}
