package camera

import (
	"image"
	"time"

	"github.com/blackjack/webcam"
)

type Format uint32

const (
	FORMAT_YUV_422 = Format(0x56595559)
)

type Camera struct {
	webcam   *webcam.Webcam
	pictures chan<- image.Image

	config CameraConfig

	loopChan chan struct{}
}

type CameraConfig struct {
	Device string `kong:"help='The video device to use.',default='/dev/video0',name='camera-device'"`
	// TODO: Make format selection configurable.
	Format      Format
	FrameWidth  uint32  `kong:"help='The width of the frame.',default=2304,name='camera-frame-width'"`
	FrameHeight uint32  `kong:"help='The height of the frame.',default=1536,name='camera-frame-height'"`
	FrameRate   float32 `kong:"help='The frame rate of the camera.',default=2.0,name='camera-frame-rate'"`

	PictureInterval time.Duration `kong:"help='The interval at which to take pictures.',default=10s,name='camera-picture-interval'"`
}

func NewCamera(cfg CameraConfig) (*Camera, error) {
	cam, err := webcam.Open(cfg.Device)
	if err != nil {
		return nil, err
	}

	return &Camera{
		webcam: cam,
		config: cfg,
	}, nil
}

func (c *Camera) Start() (<-chan image.Image, error) {
	_, _, _, err := c.webcam.SetImageFormat(webcam.PixelFormat(c.config.Format), c.config.FrameWidth, c.config.FrameHeight)
	if err != nil {
		return nil, err
	}

	err = c.webcam.SetFramerate(c.config.FrameRate)
	if err != nil {
		return nil, err
	}

	pictures := make(chan image.Image)
	c.pictures = pictures

	c.loopChan = make(chan struct{})

	go c.loop()

	return pictures, c.webcam.StartStreaming()
}

func (c *Camera) Stop() error {
	close(c.loopChan)
	close(c.pictures)

	return c.webcam.StopStreaming()
}

func (c *Camera) Close() error {
	return c.webcam.Close()
}

func (c *Camera) loop() {
	ticker := time.NewTicker(c.config.PictureInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.loopChan:
			return
		default:
			err := c.webcam.WaitForFrame(5)
			if err != nil {
				continue
			}

			frame, err := c.webcam.ReadFrame()
			if err != nil {
				continue
			}

			select {
			case <-ticker.C:
				img := encodeFrame(frame, c.config.FrameWidth, c.config.FrameHeight)
				c.pictures <- img
			default:
				continue
			}
		}
	}
}

func encodeFrame(frame []byte, w, h uint32) image.Image {
	yuyv := image.NewYCbCr(image.Rect(0, 0, int(w), int(h)), image.YCbCrSubsampleRatio422)
	for i := range yuyv.Cb {
		ii := i * 4
		yuyv.Y[i*2] = frame[ii]
		yuyv.Y[i*2+1] = frame[ii+2]
		yuyv.Cb[i] = frame[ii+1]
		yuyv.Cr[i] = frame[ii+3]
	}

	return yuyv
}
