// Package pdfimage pulls the first photo out of a PDF without rendering it:
// PDFs made from photos (pombo's, most scanner apps') embed each page as a
// JPEG stream that can be handed to the LLM as is.
package pdfimage

import (
	"bytes"
	"regexp"
	"strconv"
)

var length = regexp.MustCompile(`/Length\s+(\d+)(\s+\d+\s+R)?`)

// FirstJPEG returns the first JPEG in data of at least minSize bytes (smaller
// ones are logos and icons), or nil. Streams whose length is stored
// elsewhere in the file are skipped.
func FirstJPEG(data []byte, minSize int) []byte {
	rest := data
	for {
		i := bytes.Index(rest, []byte("/DCTDecode"))
		if i < 0 {
			return nil
		}
		start := bytes.LastIndex(rest[:i], []byte("<<"))
		end := bytes.Index(rest[i:], []byte(">>"))
		if start < 0 || end < 0 {
			return nil
		}
		dict, after := rest[start:i+end], rest[i+end+2:]
		rest = after

		m := length.FindSubmatch(dict)
		if m == nil || m[2] != nil {
			continue
		}
		n, _ := strconv.Atoi(string(m[1]))
		after = bytes.TrimLeft(after, " \r\n")
		if !bytes.HasPrefix(after, []byte("stream")) {
			continue
		}
		after = after[len("stream"):]
		after = bytes.TrimPrefix(after, []byte("\r"))
		after = bytes.TrimPrefix(after, []byte("\n"))
		if n < minSize || n > len(after) {
			continue
		}
		jpeg := after[:n]
		if !bytes.HasPrefix(jpeg, []byte{0xFF, 0xD8}) {
			continue
		}
		return jpeg
	}
}
