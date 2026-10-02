package model

const (
	MaxFileBytes      = 20 * 1024 * 1024
	MaxImageBytes     = 8 * 1024 * 1024
	MaxTextBytes      = 10 * 1024 * 1024
	MaxPageTextBytes  = 1024 * 1024
	ExtractionVersion = 1
)

type Execution struct {
	AttemptID    string `json:"attempt_id"`
	WorkerID     string `json:"worker_id"`
	FencingToken string `json:"fencing_token"`
	Generation   uint64 `json:"generation"`
}
type Manifest struct {
	MediaType      string `json:"media_type"`
	Filename       string `json:"filename"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"size_bytes"`
	DepotFileID    string `json:"depot_file_id"`
	PageCount      int    `json:"page_count"`
	CompletedPages []int  `json:"completed_pages"`
	Version        int    `json:"version"`
}
type Page struct {
	Execution
	NativeText  string `json:"native_text"`
	OCRText     string `json:"ocr_text"`
	Text        string `json:"text"`
	TextMethod  string `json:"text_method"`
	SHA256      string `json:"sha256"`
	DepotFileID string `json:"depot_file_id"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ImageBase64 string `json:"image_base64,omitempty"`
	Image       []byte `json:"-"`
}
