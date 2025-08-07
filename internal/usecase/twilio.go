package usecase

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// Storage abstracts file upload behavior for recordings.
type Storage interface {
	Upload(objectKey string, contentType string, body []byte) error
}

// TwilioService defines Twilio-related operations used by HTTP layer.
type TwilioService interface {
	StartCallRecording(callSid string, absoluteCallbackURL string) error
	UploadRecordingToStorage(recordingURL string, fileName string) error
	BuildAbsoluteURL(c echo.Context, path string) string
	DestinationNumber() string
}

type twilioService struct {
	accountSID        string
	authToken         string
	storage           Storage
	destinationNumber string
}

func NewTwilioService(accountSID, authToken, destinationNumber string, storage Storage) TwilioService {
	return &twilioService{
		accountSID:        accountSID,
		authToken:         authToken,
		destinationNumber: destinationNumber,
		storage:           storage,
	}
}

func (s *twilioService) DestinationNumber() string { return s.destinationNumber }

// BuildAbsoluteURL builds a public absolute URL for callbacks.
// Priority: BASE_URL env > X-Forwarded-* headers > request Host heuristic.
func (s *twilioService) BuildAbsoluteURL(c echo.Context, path string) string {
	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		proto := c.Request().Header.Get("X-Forwarded-Proto")
		host := c.Request().Header.Get("X-Forwarded-Host")
		if proto != "" && host != "" {
			baseURL = fmt.Sprintf("%s://%s", proto, host)
		}
	}
	if baseURL == "" {
		host := c.Request().Host
		proto := "https"
		if strings.HasPrefix(host, "localhost:") || strings.HasPrefix(host, "127.0.0.1:") {
			proto = "http"
		}
		baseURL = fmt.Sprintf("%s://%s", proto, host)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return baseURL + path
}

// StartCallRecording creates a single continuous recording on an in-progress call via Twilio's REST API.
func (s *twilioService) StartCallRecording(callSid, absoluteCallbackURL string) error {
	if s.accountSID == "" || s.authToken == "" {
		return fmt.Errorf("missing Twilio credentials: TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN required to start recording")
	}

	apiURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Calls/%s/Recordings.json", s.accountSID, callSid)
	data := url.Values{}
	data.Set("RecordingStatusCallback", absoluteCallbackURL)
	data.Set("RecordingStatusCallbackMethod", "POST")
	data.Set("RecordingStatusCallbackEvent", "in-progress completed absent")
	data.Set("Trim", "do-not-trim")
	data.Set("RecordingChannels", "mono")
	data.Set("RecordingTrack", "both")

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create start recording request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.accountSID, s.authToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to start recording: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyPreview, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("start recording failed with status %d: %s", resp.StatusCode, string(bodyPreview))
	}
	return nil
}

// UploadRecordingToStorage downloads Twilio recording and uploads to storage backend.
func (s *twilioService) UploadRecordingToStorage(recordingURL, fileName string) error {
	if s.accountSID == "" || s.authToken == "" {
		return fmt.Errorf("missing Twilio credentials: TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN required to download recording")
	}
	mediaURL := recordingURL + ".wav"
	req, err := http.NewRequest("GET", mediaURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request to Twilio recording URL: %v", err)
	}
	req.SetBasicAuth(s.accountSID, s.authToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download recording: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyPreview, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to download recording, status %d: %s", resp.StatusCode, string(bodyPreview))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read recording: %v", err)
	}

	if err := s.storage.Upload(fileName, "audio/wav", body); err != nil {
		return fmt.Errorf("failed to upload to storage: %v", err)
	}
	return nil
}
