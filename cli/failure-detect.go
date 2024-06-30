package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/fogleman/gg"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	mlAPIDurationHistogram = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "prusalgtm",
			Name:      "mlapi_request_duration_seconds",
			Help:      "A histogram of request latencies to the ML API.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"code", "method"},
	)

	mlAPILastFailuresCount = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "prusalgtm",
		Name:      "mlapi_last_failures_count",
		Help:      "The number of failures detected in the last request to the ML API.",
	})
	mlAPILastCallSuccessTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "prusalgtm",
		Name:      "mlapi_last_call_success_timestamp_seconds",
		Help:      "The timestamp of the last successful call to the ML API.",
	})

	mlAPIRoundTripper = promhttp.InstrumentRoundTripperDuration(mlAPIDurationHistogram, http.DefaultTransport)
)

type failureDetectCommand struct {
	MLAPIURL string `kong:"help='The URL to the ML API to detect failures.',required,name='ml-api-url'"`

	ImagePath string `kong:"help='The path to the image to detect failures in.',required,name='image-path',type='existingfile'"`
}

func (f *failureDetectCommand) Run() error {
	detector, err := newFailureDetector(f.MLAPIURL)
	if err != nil {
		return err
	}

	file, err := os.Open(f.ImagePath)
	if err != nil {
		return err
	}
	defer file.Close()

	img, err := jpeg.Decode(file)
	if err != nil {
		return err
	}

	_, failures, err := detector.DetectFailure(img)
	if err != nil {
		return err
	}

	for _, failure := range failures {
		fmt.Printf("Failure detected with confidence %f at coordinates %v\n", failure.Confidence, failure.BoxCoordinates)
	}

	return nil

}

type failureDetector struct {
	MLAPIURL *url.URL
}

func newFailureDetector(mlAPIURL string) (*failureDetector, error) {
	parsedURL, err := url.Parse(mlAPIURL)
	if err != nil {
		return nil, err
	}

	return &failureDetector{
		MLAPIURL: parsedURL.JoinPath("/predict"),
	}, nil
}

func (f *failureDetector) DetectFailure(img image.Image) (image.Image, []detectedFailure, error) {
	buf := bytes.NewBuffer(nil)
	jpeg.Encode(buf, img, &jpeg.Options{Quality: 100})

	client := http.DefaultClient
	client.Timeout = 5 * time.Second
	client.Transport = mlAPIRoundTripper

	resp, err := client.Post(f.MLAPIURL.String(), "image/jpeg", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("expected status code 200, got %d", resp.StatusCode)
	}

	var detectionResponse detectionResponse

	if err := json.NewDecoder(resp.Body).Decode(&detectionResponse); err != nil {
		return nil, nil, err
	}

	failures := make([]detectedFailure, 0, len(detectionResponse.Detections))
	for _, detection := range detectionResponse.Detections {
		confidence, ok := detection[1].(float64)
		if !ok {
			return nil, nil, fmt.Errorf("expected confidence to be a float64, got %T", detection[1])
		}

		boxCoordinates, ok := detection[2].([]interface{})
		if !ok {
			return nil, nil, fmt.Errorf("expected box coordinates to be a []float64, got %T", detection[2])
		}

		failures = append(failures, detectedFailure{
			Confidence:     confidence,
			BoxCoordinates: [4]float64{boxCoordinates[0].(float64), boxCoordinates[1].(float64), boxCoordinates[2].(float64), boxCoordinates[3].(float64)},
		})
	}

	mlAPILastCallSuccessTimestamp.SetToCurrentTime()
	mlAPILastFailuresCount.Set(float64(len(failures)))

	if len(failures) == 0 {
		return img, failures, nil
	}

	ggCtx := gg.NewContextForImage(img)
	ggCtx.SetColor(color.RGBA{255, 0, 0, 255})
	ggCtx.SetLineWidth(2)
	for _, failure := range failures {
		// It's the x, y (of the center of the box), width, height
		xc := failure.BoxCoordinates[0]
		yc := failure.BoxCoordinates[1]
		w := failure.BoxCoordinates[2]
		h := failure.BoxCoordinates[3]

		ggCtx.DrawRectangle(xc-(w/2), yc-(h/2), w, h)
		ggCtx.Stroke()
	}

	return ggCtx.Image(), failures, nil
}

type detectionResponse struct {
	Detections [][]any `json:"detections"`
}

type detectedFailure struct {
	Confidence     float64
	BoxCoordinates [4]float64
}
