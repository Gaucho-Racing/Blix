package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/ingestion/model"
)

var ErrInvalidFile = errors.New("invalid file")

type Processor interface {
	Extract(context.Context, string) ([]string, error)
	Page(context.Context, string, int, string) (model.Page, error)
}
type PDF struct{}

func Hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

type limitedOutput struct {
	bytes.Buffer
	limit int
}

func (w *limitedOutput) Write(data []byte) (int, error) {
	if w.Len()+len(data) > w.limit {
		return 0, fmt.Errorf("%w: extraction exceeds text limit", ErrInvalidFile)
	}
	return w.Buffer.Write(data)
}
func (PDF) Extract(ctx context.Context, path string) ([]string, error) {
	extractCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	output := &limitedOutput{limit: model.MaxTextBytes}
	command := exec.CommandContext(extractCtx, "pdftotext", "-layout", "-enc", "UTF-8", path, "-")
	command.Stdout = output
	if err := command.Run(); err != nil {
		if extractCtx.Err() != nil {
			return nil, fmt.Errorf("extract PDF: %w", extractCtx.Err())
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("pdftotext is unavailable: %w", err)
		}
		return nil, fmt.Errorf("%w: PDF could not be extracted; use an unencrypted PDF", ErrInvalidFile)
	}
	text := output.String()
	if !validText(text) {
		return nil, fmt.Errorf("%w: invalid text encoding", ErrInvalidFile)
	}
	pages := strings.Split(text, "\f")
	if len(pages) > 0 && strings.TrimSpace(pages[len(pages)-1]) == "" {
		pages = pages[:len(pages)-1]
	}
	if len(pages) < 1 || len(pages) > 500 {
		return nil, fmt.Errorf("%w: PDF must have 1–500 pages", ErrInvalidFile)
	}
	for i, page := range pages {
		pages[i] = strings.TrimSpace(page)
		if len(pages[i]) > model.MaxPageTextBytes {
			return nil, fmt.Errorf("%w: page text is too large", ErrInvalidFile)
		}
	}
	return pages, nil
}
func (PDF) Page(ctx context.Context, path string, number int, native string) (model.Page, error) {
	var page model.Page
	directory, err := os.MkdirTemp("", "blix-page-*")
	if err != nil {
		return page, err
	}
	defer os.RemoveAll(directory)
	renderCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	prefix := filepath.Join(directory, "page")
	command := exec.CommandContext(renderCtx, "pdftoppm", "-f", strconv.Itoa(number), "-l", strconv.Itoa(number), "-singlefile", "-scale-to", "2400", "-jpeg", "-jpegopt", "quality=90", path, prefix)
	if err := command.Run(); err != nil {
		return page, fmt.Errorf("render PDF page %d: %w", number, err)
	}
	file, err := os.Open(prefix + ".jpg")
	if err != nil {
		return page, err
	}
	data, err := io.ReadAll(io.LimitReader(file, model.MaxImageBytes+1))
	file.Close()
	if err != nil {
		return page, err
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || len(data) > model.MaxImageBytes || config.Width < 1 || config.Height < 1 || config.Width > 2400 || config.Height > 2400 {
		return page, fmt.Errorf("%w: invalid rendered page image", ErrInvalidFile)
	}
	page = model.Page{NativeText: native, Text: native, TextMethod: "native_text", SHA256: Hash(data), Width: config.Width, Height: config.Height, Image: data}
	if utf8.RuneCountInString(strings.TrimSpace(native)) < 80 || strings.Count(native, "\ufffd") > 3 {
		page.OCRText, err = recognize(ctx, prefix+".jpg")
		if err != nil {
			return page, err
		}
		if utf8.RuneCountInString(page.OCRText) > utf8.RuneCountInString(native) {
			page.Text = page.OCRText
			page.TextMethod = "ocr"
		}
	}
	return page, nil
}
func recognize(ctx context.Context, path string) (string, error) {
	ocrCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	output := &limitedOutput{limit: model.MaxPageTextBytes}
	command := exec.CommandContext(ocrCtx, "tesseract", path, "stdout", "-l", "eng", "--psm", "3")
	command.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	command.Stdout = output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("OCR failed: %w", err)
	}
	text := strings.TrimSpace(output.String())
	if !validText(text) {
		return "", fmt.Errorf("%w: OCR returned invalid text", ErrInvalidFile)
	}
	return text, nil
}
func validText(text string) bool { return utf8.ValidString(text) && !strings.ContainsRune(text, 0) }
func CheckDependencies() error {
	for _, name := range []string{"pdftotext", "pdftoppm", "tesseract"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("%s is required: %w", name, err)
		}
	}
	return nil
}
