package service

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gaucho-racing/blix/knowledge/model"
	"github.com/gaucho-racing/blix/knowledge/pkg/identifier"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const sourceChunkerVersion = 1
const sourcePassageBytes = 2400
const sourcePassageOverlap = 240

var paragraphBoundary = regexp.MustCompile(`\n[\t ]*\n+`)
var ruleIdentifier = regexp.MustCompile(`(?i)\b[a-z]{1,4}\.[0-9]+(?:\.[0-9]+)*\b`)

func sourceChunks(record model.SourceRecord) []model.SourceChunk {
	chunks := []model.SourceChunk{}
	text := record.Content
	for start := 0; start < len(text); {
		for start < len(text) {
			r, size := utf8.DecodeRuneInString(text[start:])
			if !unicode.IsSpace(r) {
				break
			}
			start += size
		}
		if start == len(text) {
			break
		}
		end := min(start+sourcePassageBytes, len(text))
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		if end < len(text) {
			minimum := start + sourcePassageBytes/2
			boundaries := paragraphBoundary.FindAllStringIndex(text[start:end], -1)
			if len(boundaries) > 0 && start+boundaries[len(boundaries)-1][0] >= minimum {
				end = start + boundaries[len(boundaries)-1][0]
			} else {
				for i := end; i > minimum; {
					r, size := utf8.DecodeLastRuneInString(text[start:i])
					if unicode.IsSpace(r) {
						end = i
						break
					}
					i -= size
				}
			}
		}
		content := text[start:end]
		rules := []string{}
		for _, rule := range ruleIdentifier.FindAllString(content, -1) {
			rule = strings.ToUpper(rule)
			if !slices.Contains(rules, rule) {
				rules = append(rules, rule)
			}
		}
		chunks = append(chunks, model.SourceChunk{ID: identifier.New(identifier.SourceChunk), SourceRecordID: record.ID, ChunkerVersion: sourceChunkerVersion, Ordinal: len(chunks) + 1, StartOffset: start, EndOffset: end, Content: content, ContentHash: hashBytes([]byte(content)), RuleIDs: rules})
		if end == len(text) {
			break
		}
		next := max(start+1, end-sourcePassageOverlap)
		for next < end && !utf8.RuneStart(text[next]) {
			next++
		}
		for next < end {
			r, size := utf8.DecodeRuneInString(text[next:])
			if unicode.IsSpace(r) {
				break
			}
			next += size
		}
		start = next
	}
	return chunks
}

func indexSourceRecord(tx *gorm.DB, record model.SourceRecord) error {
	var count int64
	if err := tx.Model(&model.SourceRecordIndex{}).Where("source_record_id = ? AND chunker_version = ?", record.ID, sourceChunkerVersion).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	chunks := sourceChunks(record)
	if len(chunks) > 0 {
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_record_id"}, {Name: "chunker_version"}, {Name: "ordinal"}}, DoNothing: true}).CreateInBatches(chunks, 100).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE knowledge_source_chunk SET search_vector = to_tsvector('english', content) WHERE source_record_id = ? AND chunker_version = ?", record.ID, sourceChunkerVersion).Error; err != nil {
			return err
		}
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.SourceRecordIndex{SourceRecordID: record.ID, ChunkerVersion: sourceChunkerVersion, ContentHash: record.ContentHash, ChunkCount: len(chunks)}).Error
}

func (s *SourceService) IndexPendingRecords(ctx context.Context) (int, error) {
	var records []model.SourceRecord
	if err := s.database.WithContext(ctx).Where("NOT EXISTS (SELECT 1 FROM knowledge_source_record_index i WHERE i.source_record_id = knowledge_source_record.id AND i.chunker_version = ?)", sourceChunkerVersion).Order("ingested_at ASC, id ASC").Limit(50).Find(&records).Error; err != nil {
		return 0, err
	}
	for _, record := range records {
		if err := s.database.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return indexSourceRecord(tx, record) }); err != nil {
			return 0, err
		}
	}
	return len(records), nil
}

func (s *SourceService) RunIndex(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		count, err := s.IndexPendingRecords(ctx)
		if err != nil && ctx.Err() == nil {
			report(err)
		}
		if err == nil && count == 50 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
