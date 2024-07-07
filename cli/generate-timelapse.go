package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/loki/pkg/loghttp"
	"github.com/icza/mjpeg"
)

const (
	initialFPS = 24
)

type generateTimelapseCommand struct {
	LokiURL      string `kong:"help='The URL to the Loki API to fetch logs from.',required,name='loki-url'"`
	LokiUsername string `kong:"help='The username to authenticate with the Loki API.',optional,name='loki-username'"`
	LokiPassword string `kong:"help='The password to authenticate with the Loki API.',optional,name='loki-password'"`

	LogQLQuery string    `kong:"help='The LogQL query to fetch logs.',default='{unit=\"prusaLGTM.service\"} |= \"base64\"',name='logql-query'"`
	StartTime  time.Time `kong:"help='The start time of the logs to fetch.',required,name='start-time'"`
	EndTime    time.Time `kong:"help='The end time of the logs to fetch.',required,name='end-time'"`

	EncodeToMP4 bool   `kong:"help='Whether to encode the timelapse to MP4. Requires ffmpeg',default='false',name='encode-to-mp4'"`
	OutputPath  string `kong:"help='The path to save the timelapse video.',default='videos/',name='output-path'"`
}

func (g *generateTimelapseCommand) Run() error {
	client := newLokiClient(g.LokiURL, g.LogQLQuery, g.LokiUsername, g.LokiPassword)

	// First seek to the first line.
	resp, err := client.fetchLogs(g.StartTime, g.EndTime, 1)
	if err != nil {
		return fmt.Errorf("failed to fetch logs: %w", err)
	}
	if resp.Data.Result.Type() != loghttp.ResultTypeStream {
		return fmt.Errorf("unexpected result type: %s", resp.Data.ResultType)
	}
	streams := resp.Data.Result.(loghttp.Streams)
	if len(streams) == 0 {
		fmt.Printf("No logs found. from=%s, to=%s, query=%s\n", g.StartTime, g.EndTime, g.LogQLQuery)
		return nil
	}
	if len(streams) > 1 {
		return fmt.Errorf("unexpected number of streams: %d", len(streams))
	}
	stream := streams[0]
	if len(stream.Entries) == 0 {
		fmt.Printf("No logs found. from=%s, to=%s, query=%s\n", g.StartTime, g.EndTime, g.LogQLQuery)
		return nil
	}
	start := stream.Entries[0].Timestamp.Add(-10 * time.Second)

	printCount := 0
	var timeLapse timelapseFile

	// Each line is 200KB, so we fetch 5mins at once.
	for start.Before(g.EndTime) {
		end := start.Add(5 * time.Minute)
		if end.After(g.EndTime) {
			end = g.EndTime
		}

		resp, err := client.fetchLogs(start, end, 1000)
		if err != nil {
			return fmt.Errorf("failed to fetch logs: %w", err)
		}
		start = end

		if resp.Data.Result.Type() != loghttp.ResultTypeStream {
			return fmt.Errorf("unexpected result type: %s", resp.Data.ResultType)
		}

		streams := resp.Data.Result.(loghttp.Streams)
		if len(streams) == 0 {
			// We found no logs for this 5min period. Close any open timelapse writers.
			if !timeLapse.isEmpty() {
				fmt.Printf("timelapse generated: %d\n", printCount)
				printCount++
				if err := timeLapse.close(); err != nil {
					return fmt.Errorf("failed to close timelapse writer: %w", err)
				}
				if g.EncodeToMP4 {
					if err := encodeToMP4(timeLapse.fileName); err != nil {
						return err
					}
				}

				timeLapse = timelapseFile{}
			}

			continue
		}
		if len(streams) > 1 {
			return fmt.Errorf("unexpected number of streams: %d", len(streams))
		}

		if timeLapse.isEmpty() {
			fmt.Printf("timelapse started: %d\n", printCount)

			timeLapse, err = newTimelapseFile(path.Join(g.OutputPath, fmt.Sprintf("timelapse-%d-%s.avi", printCount, start.Format("2006-01-02"))))
			if err != nil {
				return fmt.Errorf("failed to create mjpeg writer: %w", err)
			}
		}

		stream := streams[0]
		for _, entry := range stream.Entries {
			base64Image := strings.TrimPrefix(entry.Line, formatString)
			imgBytes, err := base64.StdEncoding.DecodeString(base64Image)
			if err != nil {
				return fmt.Errorf("failed to decode base64 image: %w", err)
			}
			_, err = jpeg.Decode(bytes.NewReader(imgBytes))
			if err != nil {
				return fmt.Errorf("failed to decode jpeg image: %w", err)
			}

			if err := timeLapse.addFrame(imgBytes); err != nil {
				return fmt.Errorf("failed to add image to timelapse: %w", err)
			}
		}
	}
	if !timeLapse.isEmpty() {
		fmt.Printf("timelapse generated: %d\n", printCount)
		if err := timeLapse.close(); err != nil {
			return fmt.Errorf("failed to close timelapse writer: %w", err)
		}
		if g.EncodeToMP4 {
			if err := encodeToMP4(timeLapse.fileName); err != nil {
				return err
			}
		}
	}

	return nil
}

func encodeToMP4(timelapseFileName string) error {
	// Execute ffmpeg -i input.avi -c:v mpeg4 output.mp4
	outputFile := fmt.Sprintf("%s.mp4", strings.TrimSuffix(timelapseFileName, ".avi"))

	tmpFile := fmt.Sprintf("%s.tmp.mp4", outputFile)
	cmd := exec.Command("ffmpeg", "-i", timelapseFileName, "-c:v", "mpeg4", "-qscale", "0", tmpFile)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to reencode timelapse: %w, command: %s", err, cmd.String())
	}

	if err := os.Rename(tmpFile, outputFile); err != nil {
		return fmt.Errorf("failed to rename timelapse file: %w", err)
	}

	return nil
}

type timelapseFile struct {
	fileName        string
	timeLapseWriter mjpeg.AviWriter
	framesInVideo   int
}

func newTimelapseFile(fileName string) (timelapseFile, error) {
	timeLapseWriter, err := mjpeg.New(fileName, 1920, 1080, initialFPS)
	if err != nil {
		return timelapseFile{}, fmt.Errorf("failed to create mjpeg writer: %w", err)
	}

	return timelapseFile{
		fileName:        fileName,
		timeLapseWriter: timeLapseWriter,
		framesInVideo:   0,
	}, nil
}

func (t *timelapseFile) addFrame(imgBytes []byte) error {
	if err := t.timeLapseWriter.AddFrame(imgBytes); err != nil {
		return fmt.Errorf("failed to add image to timelapse: %w", err)
	}
	t.framesInVideo++
	return nil
}

func (t *timelapseFile) close() error {
	if err := t.timeLapseWriter.Close(); err != nil {
		return fmt.Errorf("failed to close timelapse writer: %w", err)
	}

	return nil
}

func (t *timelapseFile) isEmpty() bool {
	return t.timeLapseWriter == nil
}

type lokiClient struct {
	URL   string
	query string

	client *http.Client
}

func newLokiClient(url, query, username, password string) *lokiClient {
	client := &http.Client{
		Transport: http.DefaultTransport,
	}
	if username != "" && password != "" {
		client.Transport = newBasicAuthRoundTripper(username, password, client.Transport)
	}

	return &lokiClient{
		URL:    url,
		query:  query,
		client: client,
	}
}

func (l *lokiClient) fetchLogs(start, end time.Time, limit int) (*loghttp.QueryResponse, error) {
	query_url, err := url.JoinPath(l.URL, "/loki/api/v1/query_range")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, query_url, nil)
	if err != nil {
		return nil, err
	}

	queryParams := req.URL.Query()
	queryParams.Add("query", l.query)
	queryParams.Add("start", start.Format(time.RFC3339))
	queryParams.Add("end", end.Format(time.RFC3339))
	queryParams.Add("direction", "forward")
	queryParams.Add("limit", strconv.Itoa(limit))

	req.URL.RawQuery = queryParams.Encode()

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch logs. status: %s", resp.Status)
	}

	queryResp := &loghttp.QueryResponse{}
	if err := json.NewDecoder(resp.Body).Decode(queryResp); err != nil {
		return nil, err
	}

	return queryResp, nil
}

type basicAuthRoundTripper struct {
	username string
	password string
	rt       http.RoundTripper
}

// newBasicAuthRoundTripper will apply a BASIC auth authorization header to a request unless it has
// already been set.
// Copied from https://github.com/prometheus/common/blob/8742f090d9fd2cea7e53a4fd58ae5fb6c6634336/config/http_config.go#L823-L864
func newBasicAuthRoundTripper(username string, password string, rt http.RoundTripper) http.RoundTripper {
	return &basicAuthRoundTripper{username, password, rt}
}

func (rt *basicAuthRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(req.Header.Get("Authorization")) != 0 {
		return rt.rt.RoundTrip(req)
	}

	req.SetBasicAuth(rt.username, rt.password)
	return rt.rt.RoundTrip(req)
}
