package depot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPageImagesUsePrivateDepotStorageAndVerifyCompletedUpload(t *testing.T) {
	data := []byte("jpeg-page-bytes")
	var baseURL string
	wrongSize := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/presigned" {
			if r.Method != "PUT" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "image/jpeg" {
				t.Error("presigned upload credentials or type incorrect")
			}
			received, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(data, received) {
				t.Error("page image bytes changed")
			}
			w.WriteHeader(200)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sentinel-service" {
			t.Error("missing existing Sentinel service token")
		}
		switch r.URL.Path {
		case "/buckets/blix/uploads":
			var intent map[string]any
			if err := json.NewDecoder(r.Body).Decode(&intent); err != nil {
				t.Fatal(err)
			}
			if intent["public"] != false || intent["content_type"] != "image/jpeg" || intent["tags"].(map[string]any)["blix_document_id"] != "document-test" {
				t.Error("page image must be private and tied to document")
			}
			json.NewEncoder(w).Encode(map[string]any{"file": map[string]any{"id": "file_image"}, "upload_url": baseURL + "/presigned", "method": "PUT"})
		case "/buckets/blix/uploads/file_image/complete":
			size := len(data)
			if wrongSize {
				size++
			}
			json.NewEncoder(w).Encode(File{ID: "file_image", Status: "ACTIVE", ContentType: "image/jpeg", SizeBytes: int64(size)})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	baseURL = server.URL
	client := NewClient(baseURL, "blix", "sentinel-service")
	file, err := client.UploadPageImage(context.Background(), "page.jpg", "document-test", data)
	if err != nil || file.ID != "file_image" {
		t.Fatalf("image upload: %+v %v", file, err)
	}
	wrongSize = true
	if _, err := client.UploadPageImage(context.Background(), "page.jpg", "document-test", data); err == nil {
		t.Fatal("changed image size accepted")
	}
}
