package zip_streamer

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/getsentry/sentry-go"
)

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

func (z *ZipStream) StreamAllFiles(req *http.Request) error {
	hub := sentry.GetHubFromContext(req.Context())

	zipWriter := zip.NewWriter(z.destination)
	success := 0

	for _, entry := range z.entries {
		resp, err := http.Get(entry.Url().String())
		if err != nil {
			if hub != nil {
				hub.CaptureException(err)
			}
			// TODO continue?
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if hub != nil {
				hub.CaptureMessage(fmt.Sprintf("Received status %d for URL %s", resp.StatusCode, entry.Url().String()))
			}
			// TODO continue?
			continue
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

		_, err = io.Copy(entryWriter, resp.Body)
		if err != nil {
			return err
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
