package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"os"
	"time"

	"github.com/blackjack/webcam"
	"github.com/disintegration/imaging"
)

func main() {
	cam, err := webcam.Open("/dev/video0")
	checkErr(err)

	defer cam.Close()

	format := webcam.PixelFormat(1196444237) // This is Motion-JPEG on starfive board

	_, _, _, err = cam.SetImageFormat(format, 1920, 1080)
	checkErr(err)

	checkErr(cam.SetFramerate(0.1))

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		checkErr(cam.StartStreaming())
		err = cam.WaitForFrame(5)
		switch err.(type) {
		case nil:
		case *webcam.Timeout:
			fmt.Fprint(os.Stderr, err.Error())
			continue
		default:
			panic(err.Error())
		}

		frame, err := cam.ReadFrame()
		checkErr(err)

		img, err := jpeg.Decode(bytes.NewReader(frame))
		if err != nil {
			fmt.Println("Error decoding frame", err)
			checkErr(cam.StopStreaming())
			continue
		}

		checkErr(cam.StopStreaming())

		dstImage := imaging.Resize(img, 0, 720, imaging.Lanczos)

		buf := new(bytes.Buffer)
		checkErr(jpeg.Encode(buf, dstImage, nil))

		// base64 encode the buffer
		fmt.Println("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()))
	}
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}
