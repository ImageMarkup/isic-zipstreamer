package zip_streamer

import (
	"encoding/json"
	"io/ioutil"
	"strings"
)

type ZipDescriptor struct {
	suggestedFilenameRaw string
	files                chan *FileEntry
}

func NewZipDescriptor() *ZipDescriptor {
	zd := ZipDescriptor{
		suggestedFilenameRaw: "",
		files:                make(chan *FileEntry),
	}

	return &zd
}

// Filename limited to US-ASCII per https://www.rfc-editor.org/rfc/rfc2183#section-2.3
// Filter " as it's used to quote this filename
func (zd ZipDescriptor) EscapedSuggestedFilename() string {
	rawFilename := zd.suggestedFilenameRaw
	escapedFilenameBuilder := make([]rune, 0, len(rawFilename))
	for _, r := range rawFilename {
		// Printable ASCII chars, no double quote
		if r > 31 && r < 127 && r != '"' {
			escapedFilenameBuilder = append(escapedFilenameBuilder, r)
		}
	}
	escapedFilename := string(escapedFilenameBuilder)

	if escapedFilename != "" && escapedFilename != ".zip" {
		if strings.HasSuffix(escapedFilename, ".zip") {
			return escapedFilename
		} else {
			return escapedFilename + ".zip"
		}
	}

	return "archive.zip"
}

func (zd ZipDescriptor) Files() chan *FileEntry {
	return zd.files
}

type jsonZipEntry struct {
	DeprecatedCapitalizedUrl     string `json:"Url"`
	DeprecatedCapitalizedZipPath string `json:"ZipPath"`
	Url                          string `json:"url"`
	ZipPath                      string `json:"zipPath"`
}

type jsonZipPayload struct {
	Next              *string        `json:"next"`
	Previous          *string        `json:"previous"`
	Results           []jsonZipEntry `json:"results"`
	DeprecatedEntries []jsonZipEntry `json:"entries"`
}

func UnmarshalJsonZipDescriptor(payload []byte) (*ZipDescriptor, error) {
	var parsed jsonZipPayload
	err := json.Unmarshal(payload, &parsed)
	if err != nil {
		return nil, err
	}

	zd := NewZipDescriptor()

	// Maintain backwards compatibility when files were named `entries`
	go func() {
		for {
			jsonZipFileList := parsed.Results
			if len(jsonZipFileList) == 0 {
				jsonZipFileList = parsed.DeprecatedEntries
			}

			for _, jsonZipFileItem := range jsonZipFileList {
				// Maintain backwards compatibility for non camel case parameters
				jsonFileItemUrl := jsonZipFileItem.Url
				if jsonFileItemUrl == "" {
					jsonFileItemUrl = jsonZipFileItem.DeprecatedCapitalizedUrl
				}
				jsonFileItemZipPath := jsonZipFileItem.ZipPath
				if jsonFileItemZipPath == "" {
					jsonFileItemZipPath = jsonZipFileItem.DeprecatedCapitalizedZipPath
				}

				fileEntry, err := NewFileEntry(jsonFileItemUrl, jsonFileItemZipPath)
				if err == nil {
					zd.files <- fileEntry
				}
			}

			if parsed.Next == nil {
				break
			} else {
				resp, err := retryableGet(*parsed.Next)
				if err != nil {
					// TODO: error channel
					break
				}

				if resp == nil {
					break
				}

				body, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					break
				}
				err = json.Unmarshal(body, &parsed)
				if err != nil {
					break
				}
				resp.Body.Close()
			}
		}

		close(zd.files)
	}()

	return zd, nil
}
