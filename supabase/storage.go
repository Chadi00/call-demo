package supabase

import (
	"bytes"
	"fmt"

	"github.com/supabase-community/supabase-go"
)

type Config struct {
	URL            string
	ServiceRoleKey string
	Bucket         string
}

type Storage struct {
	client *supabase.Client
	bucket string
}

func New(config Config) *Storage {
	client, err := supabase.NewClient(config.URL, config.ServiceRoleKey, &supabase.ClientOptions{})
	if err != nil {
		panic(fmt.Sprintf("Failed to create Supabase client: %v", err))
	}

	return &Storage{
		client: client,
		bucket: config.Bucket,
	}
}

func (s *Storage) Upload(key, contentType string, data []byte) error {
	_, err := s.client.Storage.UploadFile(s.bucket, key, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to upload to Supabase: %w", err)
	}
	return nil
}
