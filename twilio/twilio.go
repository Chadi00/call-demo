package twilio

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/twilio/twilio-go"
	twilioApi "github.com/twilio/twilio-go/rest/api/v2010"
)

type Storage interface {
	Upload(key, contentType string, data []byte) error
}

type Config struct {
	AccountSID string
	AuthToken  string
}

type Service struct {
	config     Config
	storage    Storage
	client     *twilio.RestClient
	httpClient *http.Client
}

func New(config Config, storage Storage) *Service {
	client := twilio.NewRestClientWithParams(twilio.ClientParams{
		Username: config.AccountSID,
		Password: config.AuthToken,
	})

	return &Service{
		config:     config,
		storage:    storage,
		client:     client,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (s *Service) RegisterHandlers(e *echo.Echo) {
	e.POST("/twilio/voice", s.handleVoice, s.authMiddleware)
	e.POST("/twilio/recording-status", s.handleRecordingStatus, s.authMiddleware)
	e.POST("/twilio/recording-complete", s.handleRecordingComplete, s.authMiddleware)
}

func (s *Service) handleVoice(c echo.Context) error {
	params := c.Get("twilioParams").(map[string]string)

	callSID := params["CallSid"]
	from := params["From"]

	log.Printf("Call from %s, CallSID: %s", from, callSID)

	// Use TwiML Record verb for reliable recording
	callbackURL := buildURL(c.Request(), "/twilio/recording-status")
	twiml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Response>
  <Say>Hello! Your call is being recorded. Please speak your message after the beep, then hang up or press any key when done.</Say>
  <Record maxLength="120" action="%s" recordingStatusCallback="%s" recordingStatusCallbackMethod="POST" />
  <Say>Thank you for your call. Goodbye!</Say>
  <Hangup/>
</Response>`, buildURL(c.Request(), "/twilio/recording-complete"), callbackURL)

	return c.XML(http.StatusOK, twiml)
}

func (s *Service) handleRecordingStatus(c echo.Context) error {
	params := c.Get("twilioParams").(map[string]string)

	status := params["RecordingStatus"]
	recordingURL := params["RecordingUrl"]
	recordingSID := params["RecordingSid"]

	log.Printf("Recording status: %s, SID: %s", status, recordingSID)

	if status == "completed" && recordingURL != "" {
		filename := fmt.Sprintf("recording_%s_%d.wav", recordingSID, time.Now().Unix())
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.uploadRecording(ctx, recordingURL, filename); err != nil {
				log.Printf("Failed to upload recording: %v", err)
			} else {
				log.Printf("Recording uploaded: %s", filename)
			}
		}()
	}

	return c.String(http.StatusOK, "OK")
}

func (s *Service) handleRecordingComplete(c echo.Context) error {
	params := c.Get("twilioParams").(map[string]string)

	recordingURL := params["RecordingUrl"]
	recordingSID := params["RecordingSid"]

	log.Printf("Recording completed: SID: %s", recordingSID)

	if recordingURL != "" {
		filename := fmt.Sprintf("recording_%s_%d.wav", recordingSID, time.Now().Unix())
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.uploadRecording(ctx, recordingURL, filename); err != nil {
				log.Printf("Failed to upload recording: %v", err)
			} else {
				log.Printf("Recording uploaded: %s", filename)
			}
		}()
	}

	return c.String(http.StatusOK, "OK")
}

func (s *Service) authMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if s.config.AuthToken == "" {
			return c.String(http.StatusInternalServerError, "Missing TWILIO_AUTH_TOKEN")
		}

		body, err := io.ReadAll(c.Request().Body)
		if err != nil {
			return c.String(http.StatusBadRequest, "Failed to read body")
		}

		formData, err := url.ParseQuery(string(body))
		if err != nil {
			return c.String(http.StatusBadRequest, "Failed to parse form")
		}

		params := make(map[string]string)
		for key, values := range formData {
			if len(values) > 0 {
				params[key] = values[0]
			}
		}

		signature := c.Request().Header.Get("X-Twilio-Signature")
		requestURL := buildURL(c.Request(), c.Request().URL.Path)

		if !s.validateSignature(signature, requestURL, params) {
			return c.String(http.StatusUnauthorized, "Invalid signature")
		}

		c.Set("twilioParams", params)
		return next(c)
	}
}

func (s *Service) validateSignature(signature, url string, params map[string]string) bool {
	data := url
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		data += k + params[k]
	}

	mac := hmac.New(sha1.New, []byte(s.config.AuthToken))
	mac.Write([]byte(data))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expected))
}

func (s *Service) startRecording(callSID, callbackURL string) error {
	params := &twilioApi.CreateCallRecordingParams{}
	params.SetRecordingStatusCallback(callbackURL)
	params.SetRecordingStatusCallbackMethod("POST")
	params.SetRecordingStatusCallbackEvent([]string{"completed"})
	params.SetRecordingChannels("mono")

	_, err := s.client.Api.CreateCallRecording(callSID, params)
	if err != nil {
		return fmt.Errorf("failed to start recording: %w", err)
	}
	return nil
}

func (s *Service) uploadRecording(ctx context.Context, recordingURL, filename string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", recordingURL+".wav", nil)
	if err != nil {
		return err
	}

	req.SetBasicAuth(s.config.AccountSID, s.config.AuthToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download recording failed: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return s.storage.Upload(filename, "audio/wav", data)
}

func buildURL(r *http.Request, path string) string {
	scheme := "https"
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
		if strings.Contains(host, "localhost") || strings.Contains(host, "127.0.0.1") {
			scheme = "http"
		}
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}
