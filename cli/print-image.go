package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"time"

	"github.com/disintegration/imaging"
	"github.com/gouthamve/prusaLGTM/camera"
	"github.com/icholy/digest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	ImageSize_1080p ImageSize = 1080
	ImageSize_720p  ImageSize = 720
	ImageSize_480p  ImageSize = 480
	ImageSize_360p  ImageSize = 360
	ImageSize_240p  ImageSize = 240

	formatString = "data:image/jpeg;base64,"
)

var (
	promImagesLogged = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "prusalgtm",
			Name:      "images_logged_total",
			Help:      "The number of images logged.",
		},
		[]string{"pixels_size"},
	)
	promImagesLoggedSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "prusalgtm",
		Name:      "images_logged_size_bytes",
		Help:      "The size of the images logged.",
		Buckets:   prometheus.DefBuckets,
	})

	promPrusaLinkDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "prusalgtm",
			Name:      "prusalink_request_duration_seconds",
			Help:      "A histogram of request latencies to the PrusaLink API.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"code", "method"},
	)
)

type ImageSize int

type PrintConfig struct {
	MaxLogSize   int       `kong:"help='Maximum bytes of the image to be logged. Set it to lower than Loki log line limit',default='256000',name='max-log-size'"`
	MaxImageSize ImageSize `kong:"help='Maximum size of the image to be logged in pixels.',default='1080',name='max-image-size',enum='1080,720,480,360,240'"`

	// Need to migrate to a proper config file at this point. But delaying it with a hack. The auth is using http digest, but here I am specifying basic auth, and then changing it later.
	PrusaLinkURL *url.URL `kong:"help='The URL to PrusaLink. When provided we only log images when there is a print job ongoing.',default='',name='prusa-link-url',optional"`

	// Add ML API support.
	MLAPIURL string `kong:"help='The URL to the ML API to detect failures.',optional,name='ml-api-url'"`
}

type printImage struct {
	PrintConfig

	camera.CameraConfig
}

func (p *printImage) Run() error {
	cam, err := camera.NewCamera(p.CameraConfig)
	if err != nil {
		return err
	}
	defer cam.Close()

	var detector *failureDetector
	if p.MLAPIURL != "" {
		detector, err = newFailureDetector(p.MLAPIURL)
		if err != nil {
			return err
		}
	}

	if p.PrusaLinkURL == nil {
		pictures, err := cam.Start()
		if err != nil {
			return err
		}
		defer cam.Stop()
		return p.logImages(pictures, detector)
	}

	shouldLogImagesCh := make(chan bool)
	defer close(shouldLogImagesCh)

	go func() {
		// Call the PrusaLink API every 5 seconds to get the print status.
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()

		for range timer.C {
			isPrinting, err := isPrinterPrinting(p.PrusaLinkURL)
			if err != nil {
				fmt.Println(err)
				continue
			}

			shouldLogImagesCh <- isPrinting
		}
	}()

	return p.logImagesWhenPrinting(cam, shouldLogImagesCh, detector)
}

func (p *printImage) logImages(pictures <-chan image.Image, detector *failureDetector) error {
	maxImageBytes := p.PrintConfig.MaxLogSize - len(formatString)

	validSizes := []ImageSize{ImageSize_1080p, ImageSize_720p, ImageSize_480p, ImageSize_360p, ImageSize_240p}
	for _, size := range validSizes {
		if size <= p.PrintConfig.MaxImageSize {
			break
		}

		validSizes = validSizes[1:]
	}

	for img := range pictures {
		if detector != nil {
			image, _, err := detector.DetectFailure(img)
			if err != nil {
				fmt.Println("detection failure", err)
			} else {
				img = image
			}
		}

		for _, size := range validSizes {
			dstImage := imaging.Resize(img, 0, int(size), imaging.Lanczos)

			buf := new(bytes.Buffer)
			if err := jpeg.Encode(buf, dstImage, nil); err != nil {
				return err
			}

			if len(buf.Bytes()) < maxImageBytes {
				fmt.Println(formatString + base64.StdEncoding.EncodeToString(buf.Bytes()))
				promImagesLoggedSize.Observe(float64(len(buf.Bytes())))

				promImagesLogged.WithLabelValues(fmt.Sprintf("%d", size)).Inc()
				break
			}
		}
	}

	return nil
}

func (p *printImage) logImagesWhenPrinting(cam *camera.Camera, shouldLogImagesCh <-chan bool, detector *failureDetector) error {
	isLogging := false

	for shouldLog := range shouldLogImagesCh {
		if shouldLog && !isLogging {
			pictures, err := cam.Start()
			if err != nil {
				fmt.Println(err, "error starting camera")
				return err
			}

			isLogging = true

			go p.logImages(pictures, detector)

		} else if !shouldLog && isLogging {
			if err := cam.Stop(); err != nil {
				fmt.Println(err, "error stopping camera")
				return err
			}

			isLogging = false
		}
	}

	return nil
}

func isPrinterPrinting(prusaLinkURL *url.URL) (bool, error) {
	statusURL := prusaLinkURL.JoinPath("/api/v1/status")
	username := prusaLinkURL.User.Username()
	password, _ := prusaLinkURL.User.Password()

	client := &http.Client{
		Transport: &digest.Transport{
			Username: username,
			Password: password,
		},
	}
	client.Transport = promhttp.InstrumentRoundTripperDuration(promPrusaLinkDuration, client.Transport)
	statusURL.User = nil

	// Call the PrusaLink API to get the print status.
	resp, err := client.Get(statusURL.String())
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return false, err
	}

	if status.Printer.State == "PRINTING" || status.Printer.State == "PAUSED" || status.Printer.State == "ATTENTION" {
		return true, nil
	}
	if status.Printer.State != "OPERATIONAL" && status.Printer.State != "FINISHED" && status.Printer.State != "IDLE" {
		fmt.Println(status.Printer.State, "is an unknown state.")
	}

	return false, nil
}

type Status struct {
	Job struct {
		ID            int     `json:"id"`
		Progress      float64 `json:"progress"`
		TimeRemaining int     `json:"time_remaining"`
		TimePrinting  int     `json:"time_printing"`
	} `json:"job"`
	Storage struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
	} `json:"storage"`
	Printer struct {
		State        string  `json:"state"`
		TempBed      float64 `json:"temp_bed"`
		TargetBed    float64 `json:"target_bed"`
		TempNozzle   float64 `json:"temp_nozzle"`
		TargetNozzle float64 `json:"target_nozzle"`
		AxisZ        float64 `json:"axis_z"`
		Flow         float64 `json:"flow"`
		Speed        float64 `json:"speed"`
		FanHotend    float64 `json:"fan_hotend"`
		FanPrint     float64 `json:"fan_print"`
	} `json:"printer"`
}
