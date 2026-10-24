package pdfimage

import (
	"bytes"
	"image/jpeg"
	"os"
	"testing"
)

func TestFirstJPEGFromPombosPDF(t *testing.T) {
	data, err := os.ReadFile("testdata/pombo.pdf")
	if err != nil {
		t.Fatal(err)
	}
	img := FirstJPEG(data, 100)
	if img == nil {
		t.Fatal("no JPEG found")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(img))
	if err != nil || cfg.Width != 40 || cfg.Height != 60 {
		t.Fatalf("decoded %+v, %v", cfg, err)
	}
	if FirstJPEG(data, 1<<20) != nil {
		t.Error("returned a JPEG smaller than minSize")
	}
}

func TestFirstJPEGWithoutImages(t *testing.T) {
	for _, data := range []string{"", "%PDF-1.4 text only", "<< /Filter /DCTDecode /Length 9 0 R >>\nstream\n\xff\xd8xx"} {
		if FirstJPEG([]byte(data), 0) != nil {
			t.Errorf("found a JPEG in %q", data)
		}
	}
}
