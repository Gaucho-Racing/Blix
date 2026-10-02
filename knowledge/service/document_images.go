package service

import (
	"bytes"
	"context"
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

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const pageImageVersion = 1
const maxPageImageBytes = 8 * 1024 * 1024

func readLimitedImage(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxPageImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxPageImageBytes {
		return nil, ErrInvalid
	}
	return data, nil
}

type DocumentImageReference struct {
	DocumentID string `json:"document_id"`
	Page       int    `json:"page"`
	SHA256     string `json:"sha256"`
}

func (s *DocumentService) ChunkImages(ctx context.Context, id string, first, last int) ([]DocumentImageReference, error) {
	var assets []model.DocumentPageImage
	if err := s.db.WithContext(ctx).Where("document_id = ? AND number BETWEEN ? AND ?", id, first, last).Order("number").Find(&assets).Error; err != nil {
		return nil, err
	}
	refs := []DocumentImageReference{}
	for _, asset := range assets {
		refs = append(refs, DocumentImageReference{DocumentID: id, Page: asset.Number, SHA256: asset.SHA256})
	}
	return refs, nil
}

type PDFPageRenderer interface {
	RenderPage(context.Context, string, int) ([]byte, error)
	RecognizeText(context.Context, []byte) (string, error)
}

func (PopplerExtractor) RenderPage(ctx context.Context, path string, page int) ([]byte, error) {
	directory, err := os.MkdirTemp("", "blix-page-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	renderCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	prefix := filepath.Join(directory, "page")
	command := exec.CommandContext(renderCtx, "pdftoppm", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-singlefile", "-scale-to", "2400", "-jpeg", "-jpegopt", "quality=90", path, prefix)
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%w: PDF page %d could not be rendered", ErrUnavailable, page)
	}
	file, err := os.Open(prefix + ".jpg")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimitedImage(file)
}

func (PopplerExtractor) RecognizeText(ctx context.Context, data []byte) (string, error) {
	file, err := os.CreateTemp("", "blix-ocr-*.jpg")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ocrCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	output := &limitedOutput{limit: 1024 * 1024}
	command := exec.CommandContext(ocrCtx, "tesseract", file.Name(), "stdout", "-l", "eng", "--psm", "3")
	command.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	command.Stdout = output
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%w: OCR could not read this page", ErrUnavailable)
	}
	text := strings.TrimSpace(output.String())
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return "", fmt.Errorf("%w: OCR returned invalid text", ErrInvalid)
	}
	return text, nil
}

func requiresOCR(text string) bool {
	return utf8.RuneCountInString(strings.TrimSpace(text)) < 80 || strings.Count(text, "\ufffd") > 3
}

func (s *DocumentService) preparePageImages(ctx context.Context, doc model.Document, hash, path string, text []string, execution DocumentExecution) ([]string, error) {
	renderer, ok := s.extractor.(PDFPageRenderer)
	if !ok {
		return text, nil
	}
	result := append([]string{}, text...)
	total := 0
	for i, native := range text {
		var asset model.DocumentPageImage
		err := s.db.WithContext(ctx).First(&asset, "document_id = ? AND number = ?", doc.ID, i+1).Error
		if err == nil {
			if asset.PDFHash != hash || asset.Version != pageImageVersion {
				return nil, ErrConflict
			}
			result[i] = asset.Text
		} else {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
			if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { _, err := s.lockedExecution(ctx, tx, doc.ID, execution); return err }); err != nil {
				return nil, err
			}
			asset, err = s.createPageImage(ctx, renderer, path, doc.ID, i+1, hash, native, true)
			if err != nil {
				return nil, err
			}
			if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if _, err := s.lockedExecution(ctx, tx, doc.ID, execution); err != nil {
					return err
				}
				return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&asset).Error
			}); err != nil {
				return nil, err
			}
			result[i] = asset.Text
		}
		total += len(result[i])
		if total > 10*1024*1024 {
			return nil, fmt.Errorf("%w: extracted text exceeds size limit", ErrInvalid)
		}
	}
	return result, nil
}

func (s *DocumentService) ReadPageImage(ctx context.Context, id string, number int) ([]byte, model.DocumentPageImage, error) {
	var asset model.DocumentPageImage
	if !identifier.IsValid(id, identifier.Document) || number < 1 || number > 500 {
		return nil, asset, ErrInvalid
	}
	err := s.db.WithContext(ctx).First(&asset, "document_id = ? AND number = ?", id, number).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		asset, err = s.renderExistingPage(ctx, id, number)
	}
	if err != nil {
		return nil, asset, mapDatabaseError(err)
	}
	var data []byte
	if asset.DepotFileID != "" {
		if s.depot == nil {
			return nil, asset, ErrUnavailable
		}
		data, err = s.depot.Download(ctx, asset.DepotFileID, maxPageImageBytes)
	} else {
		file, openErr := os.Open(filepath.Join(s.directory, "pages", asset.SHA256+".jpg"))
		if openErr != nil {
			return nil, asset, ErrUnavailable
		}
		defer file.Close()
		data, err = readLimitedImage(file)
	}
	if err != nil {
		return nil, asset, fmt.Errorf("%w: page image could not be loaded", ErrUnavailable)
	}
	if hashBytes(data) != asset.SHA256 {
		return nil, asset, fmt.Errorf("%w: stored page image changed", ErrConflict)
	}
	return data, asset, nil
}

func (s *DocumentService) AgentImageScope(ctx context.Context, runID, sessionID string) (DocumentRetrievalScope, error) {
	access, err := s.agentDocumentContext(ctx, runID, sessionID)
	if err != nil {
		return DocumentRetrievalScope{}, err
	}
	if access.ProfileID == "document_ingest_v1" || access.ProfileID == "document_qa_v1" {
		if len(access.DocumentIDs) != 1 {
			return DocumentRetrievalScope{}, ErrForbidden
		}
		return DocumentRetrievalScope{DocumentIDs: access.DocumentIDs, AllowUnapproved: true}, nil
	}
	return DocumentRetrievalScope{Team: true}, nil
}

func (s *DocumentService) AuthorizeImage(ctx context.Context, id string, scope DocumentRetrievalScope) error {
	query, err := documentRetrievalQuery(s.db.WithContext(ctx), scope, id)
	if err != nil {
		return err
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *DocumentService) EvidenceImage(ctx context.Context, id string, page int, scope DocumentRetrievalScope) (map[string]any, error) {
	evidence, err := s.ReadEvidencePage(ctx, id, page, 0, scope)
	if err != nil {
		return nil, err
	}
	refs, err := s.ChunkImages(ctx, id, page, page)
	if err == nil && len(refs) == 0 {
		_, asset, imageErr := s.ReadPageImage(ctx, id, page)
		err = imageErr
		if err == nil {
			refs = append(refs, DocumentImageReference{DocumentID: id, Page: page, SHA256: asset.SHA256})
		}
	}
	if err != nil {
		return nil, err
	}
	if len(refs) != 1 {
		return nil, ErrNotFound
	}
	return map[string]any{"document_id": id, "pdf_page": page, "source_record_id": evidence.SourceRecordID, "content": evidence.Content, "citation_url": evidence.CitationURL, "image_reference": refs[0], "image_url": fmt.Sprintf("/knowledge/documents/%s/pages/%d/image", id, page)}, nil
}

func (s *DocumentService) renderExistingPage(ctx context.Context, id string, number int) (model.DocumentPageImage, error) {
	var asset model.DocumentPageImage
	var page model.DocumentPage
	if err := s.db.WithContext(ctx).First(&page, "document_id = ? AND number = ?", id, number).Error; err != nil {
		return asset, err
	}
	renderer, ok := s.extractor.(PDFPageRenderer)
	if !ok {
		return asset, ErrNotFound
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return asset, ctx.Err()
	}
	if err := s.db.WithContext(ctx).First(&asset, "document_id = ? AND number = ?", id, number).Error; err == nil {
		return asset, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return asset, err
	}
	original, _, err := s.ReadOriginal(ctx, id)
	if err != nil {
		return asset, err
	}
	file, err := os.CreateTemp("", "blix-preview-*.pdf")
	if err != nil {
		return asset, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(original); err != nil {
		file.Close()
		return asset, err
	}
	if err := file.Close(); err != nil {
		return asset, err
	}
	var record model.SourceRecord
	if err := s.db.WithContext(ctx).First(&record, "id = ?", page.SourceRecordID).Error; err != nil {
		return asset, err
	}
	asset, err = s.createPageImage(ctx, renderer, file.Name(), id, number, hashBytes(original), record.Content, false)
	if err != nil {
		return asset, err
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&asset).Error; err != nil {
		return asset, err
	}
	return asset, nil
}

func (s *DocumentService) createPageImage(ctx context.Context, renderer PDFPageRenderer, path, id string, number int, hash, native string, recognize bool) (model.DocumentPageImage, error) {
	var asset model.DocumentPageImage
	data, err := renderer.RenderPage(ctx, path, number)
	if err != nil {
		return asset, err
	}
	configuration, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || len(data) > maxPageImageBytes || configuration.Width < 1 || configuration.Height < 1 || configuration.Width > 2400 || configuration.Height > 2400 {
		return asset, fmt.Errorf("%w: invalid rendered page image", ErrInvalid)
	}
	asset = model.DocumentPageImage{DocumentID: id, Number: number, PDFHash: hash, Version: pageImageVersion, SHA256: hashBytes(data), Width: configuration.Width, Height: configuration.Height, NativeText: native, Text: native, TextMethod: "native_text"}
	if recognize && requiresOCR(native) {
		ocr, err := renderer.RecognizeText(ctx, data)
		if err != nil {
			return asset, err
		}
		if utf8.RuneCountInString(ocr) > utf8.RuneCountInString(native) {
			asset.Text = ocr
			asset.TextMethod = "ocr"
		}
	}
	if s.depot != nil && s.depot.Configured() {
		file, err := s.depot.UploadPageImage(ctx, fmt.Sprintf("%s-page-%d.jpg", id, number), id, data)
		if err != nil {
			return asset, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		asset.DepotFileID = file.ID
	} else {
		if err := os.MkdirAll(filepath.Join(s.directory, "pages"), 0700); err != nil {
			return asset, err
		}
		if err := os.WriteFile(filepath.Join(s.directory, "pages", asset.SHA256+".jpg"), data, 0600); err != nil {
			return asset, err
		}
	}
	return asset, nil
}
