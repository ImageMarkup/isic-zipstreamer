package zip_streamer

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/getsentry/sentry-go"
)

const NUM_RETRIES = 5

var retryableStatusCodes = []int{
	http.StatusRequestTimeout,
	http.StatusTooManyRequests,
	http.StatusInternalServerError,
	http.StatusBadGateway,
	http.StatusServiceUnavailable,
	http.StatusGatewayTimeout,
}

type ZipStream struct {
	entries           []*FileEntry
	destination       io.Writer
	CompressionMethod uint16
}

func NewZipStream(entries []*FileEntry, w io.Writer) (*ZipStream, error) {
	if len(entries) == 0 {
		return nil, errors.New("must have at least 1 entry")
	}

	z := ZipStream{
		entries:     entries,
		destination: w,
		// Default to no compression to save CPU. Also ideal for streaming.
		CompressionMethod: zip.Store,
	}

	return &z, nil
}

func getS3Object(urlStr string) (*http.Response, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}

	// Extract bucket name from hostname (bucket.s3.region.amazonaws.com format)
	host := parsedURL.Host
	key := strings.TrimPrefix(parsedURL.Path, "/")

	parts := strings.Split(host, ".")
	bucket := parts[0]

	// Check for region in environment variable first
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}
	if region == "" {
		region = "us-east-1"
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
	)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(cfg)

	result, err := client.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	})
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(result.Body)
	if err != nil {
		result.Body.Close()
		return nil, err
	}
	result.Body.Close()

	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	return resp, nil
}

func retryableGet(urlStr string) (*http.Response, error) {
	var err error
	var sleepDuration time.Duration

	isS3URL := strings.Contains(urlStr, "isic-storage")

	for i := 0; i < NUM_RETRIES; i++ {
		sleepDuration = time.Duration(math.Min(math.Pow(float64(2), float64(i)), float64(30))) * time.Second

		var resp *http.Response

		if isS3URL {
			resp, err = getS3Object(urlStr)
		} else {
			req, err := http.NewRequest("GET", urlStr, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", fmt.Sprintf("isic-zipstreamer/%s", getVcsRevision()))
			resp, err = http.DefaultClient.Do(req)
		}

		if err != nil {
			if i < NUM_RETRIES-1 {
				time.Sleep(sleepDuration)
			}
		} else if !isS3URL && slices.Contains(retryableStatusCodes, resp.StatusCode) {
			time.Sleep(sleepDuration)
		} else if resp != nil {
			return resp, nil
		}
	}

	if err == nil {
		err = errors.New("max retries exceeded")
	}

	return nil, err
}

func (z *ZipStream) StreamAllFiles(context context.Context) error {
	hub := sentry.GetHubFromContext(context)

	zipWriter := zip.NewWriter(z.destination)
	success := 0

	for _, entry := range z.entries {
		entryFailed := false

		resp, err := retryableGet(entry.Url().String())
		if err != nil {
			if hub != nil {
				hub.CaptureException(err)
			}
			entryFailed = true
		} else {
			defer resp.Body.Close()
		}

		header := &zip.FileHeader{
			Name:     entry.ZipPath(),
			Method:   z.CompressionMethod,
			Modified: time.Now(),
		}
		entryWriter, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}

		// print entry failed
		if entryFailed {
			fmt.Printf("Entry failed: %s\n", entry.Url().String())
		}
		// only write a file if it downloaded successfully, otherwise write it as a 0 byte file
		if !entryFailed {
			_, err = io.Copy(entryWriter, resp.Body)
			if err != nil {
				return err
			}
		}

		zipWriter.Flush()
		flushingWriter, ok := z.destination.(http.Flusher)
		if ok {
			flushingWriter.Flush()
		}

		success++
	}

	if success == 0 {
		return errors.New("empty file - all files failed")
	}

	return zipWriter.Close()
}
