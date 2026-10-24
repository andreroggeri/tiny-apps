// Package pdf combines photographed pages into a single PDF.
package pdf

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strconv"

	"github.com/go-pdf/fpdf"
)

// pageWidthMM is A4 width; each page's height follows the photo's aspect
// ratio so nothing is cropped or letterboxed.
const pageWidthMM = 210.0

// Supported reports whether FromImages can embed images of this MIME type.
func Supported(mimeType string) bool {
	return fpdfType(mimeType) != ""
}

func fpdfType(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return "JPEG"
	case "image/png":
		return "PNG"
	}
	return ""
}

type Image struct {
	Data     []byte
	MimeType string
}

// FromImages returns a PDF with one page per image, in order. A non-empty
// context adds a final text page with it, so paperless reads it along with
// the pages (for search and its AI suggestions).
func FromImages(images []Image, context string) ([]byte, error) {
	doc := fpdf.New("P", "mm", "A4", "")
	doc.SetMargins(0, 0, 0)
	doc.SetAutoPageBreak(false, 0)

	for i, img := range images {
		typ := fpdfType(img.MimeType)
		if typ == "" {
			return nil, fmt.Errorf("page %d: unsupported image type %q", i+1, img.MimeType)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		height := pageWidthMM * float64(cfg.Height) / float64(cfg.Width)

		name := "page" + strconv.Itoa(i)
		opts := fpdf.ImageOptions{ImageType: typ}
		doc.RegisterImageOptionsReader(name, opts, bytes.NewReader(img.Data))
		doc.AddPageFormat("P", fpdf.SizeType{Wd: pageWidthMM, Ht: height})
		doc.ImageOptions(name, 0, 0, pageWidthMM, height, false, opts, 0, "")
	}

	if context != "" {
		// Core fonts are cp1252: covers Portuguese accents, not emoji.
		tr := doc.UnicodeTranslatorFromDescriptor("")
		doc.AddPageFormat("P", fpdf.SizeType{Wd: pageWidthMM, Ht: 297})
		doc.SetMargins(20, 20, 20)
		doc.SetXY(20, 20)
		doc.SetFont("Helvetica", "B", 14)
		doc.MultiCell(0, 8, tr("Contexto enviado pelo WhatsApp"), "", "L", false)
		doc.Ln(4)
		doc.SetFont("Helvetica", "", 12)
		doc.MultiCell(0, 7, tr(context), "", "L", false)
	}

	var out bytes.Buffer
	if err := doc.Output(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
