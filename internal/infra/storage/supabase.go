package storage

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SupabaseStorage implements Storage using Supabase's Storage API.
type SupabaseStorage struct {
	BaseURL    string
	ServiceKey string
	Bucket     string
	Client     *http.Client
}

// NewSupabaseStorage constructs a new Supabase storage client.
func NewSupabaseStorage(baseURL, serviceKey, bucket string) *SupabaseStorage {
	return &SupabaseStorage{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		ServiceKey: serviceKey,
		Bucket:     bucket,
		Client:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *SupabaseStorage) Upload(objectKey string, contentType string, body []byte) error {
	if s.BaseURL == "" || s.ServiceKey == "" {
		return fmt.Errorf("missing Supabase configuration: SUPABASE_URL and SUPABASE_SERVICE_ROLE_KEY required")
	}

	uploadURL := fmt.Sprintf("%s/storage/v1/object/%s/%s", s.BaseURL, s.Bucket, objectKey)
	req, err := http.NewRequest("POST", uploadURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create upload request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.ServiceKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Cache-Control", "3600")
	req.Header.Set("x-upsert", "true")

	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload to Supabase: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("upload failed with status %d", resp.StatusCode)
	}
	return nil
}
