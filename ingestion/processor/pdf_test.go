package processor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testPDF() []byte {
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}
	content := "BT /F1 18 Tf 50 700 Td (Formula SAE extraction test page) Tj ET"
	objects = append(objects, "<< /Length "+itoa(len(content))+" >>\nstream\n"+content+"\nendstream")
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, object := range objects {
		offsets = append(offsets, b.Len())
		b.WriteString(itoa(i+1) + " 0 obj\n" + object + "\nendobj\n")
	}
	xref := b.Len()
	b.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		b.WriteString(fmtOffset(offset) + " 00000 n \n")
	}
	b.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n" + itoa(xref) + "\n%%EOF\n")
	return []byte(b.String())
}

func itoa(value int) string      { return strconv.Itoa(value) }
func fmtOffset(value int) string { return fmt.Sprintf("%010d", value) }
func TestPDFNativeRenderAndOCR(t *testing.T) {
	if err := CheckDependencies(); err != nil {
		t.Skip(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.pdf")
	if err := os.WriteFile(path, testPDF(), 0600); err != nil {
		t.Fatal(err)
	}
	p := PDF{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	text, err := p.Extract(ctx, path)
	if err != nil || len(text) != 1 || !strings.Contains(text[0], "Formula SAE") {
		t.Fatalf("native extraction: %v %v", text, err)
	}
	page, err := p.Page(ctx, path, 1, text[0])
	if err != nil || page.Width < 1 || len(page.Image) == 0 || page.OCRText == "" || page.NativeText != text[0] {
		t.Fatalf("render/OCR: size=%dx%d image_bytes=%d native=%q ocr=%q error=%v", page.Width, page.Height, len(page.Image), page.NativeText, page.OCRText, err)
	}
	cancel()
	if _, err := p.Extract(ctx, path); err == nil {
		t.Fatal("cancelled extraction succeeded")
	}
}
func TestMalformedPDFRejected(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip(err)
	}
	path := filepath.Join(t.TempDir(), "bad.pdf")
	if err := os.WriteFile(path, []byte("%PDF-invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (PDF{}).Extract(context.Background(), path); err == nil {
		t.Fatal("malformed PDF accepted")
	}
}
