package cli

var PrusaLGTM struct {
	PrintImage printImage `cmd:"print-image" help:"Print images from a camera to stdout."`

	FailureDetect failureDetectCommand `cmd:"failure-detect" help:"Detect failures in the print images."`

	PrometheusPort int `kong:"help='The port to expose Prometheus metrics on.',default='8366',name='prometheus-port'"`
}
