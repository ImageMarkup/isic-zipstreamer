package zip_streamer

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"time"

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

func retryableGet(url string) (*http.Response, error) {
	var err error
	var sleepDuration time.Duration

	for i := 0; i < NUM_RETRIES; i++ {
		sleepDuration = time.Duration(math.Min(math.Pow(float64(2), float64(i)), float64(30))) * time.Second

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", fmt.Sprintf("isic-zipstreamer/%s", getVcsRevision()))
		resp, err := http.DefaultClient.Do(req)

		if err != nil {
			time.Sleep(sleepDuration)
		} else if slices.Contains(retryableStatusCodes, resp.StatusCode) {
			time.Sleep(sleepDuration)
		} else {
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
