package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/twilio/twilio-go/twiml"
)

func validateTwilioSignature(authToken, signature, url string, params map[string]string) bool {
	if authToken == "" || signature == "" {
		return false
	}

	data := url
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		data += k + params[k]
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(data))
	expectedSignature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

func uploadRecordingToSupabase(recordingURL, fileName string) error {
	supabaseURL := os.Getenv("SUPABASE_URL")
	supabaseKey := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	bucketName := "voice-recording"

	if supabaseURL == "" || supabaseKey == "" {
		return fmt.Errorf("missing Supabase configuration: SUPABASE_URL and SUPABASE_SERVICE_ROLE_KEY required")
	}

	// Download the recording from Twilio using Basic Auth
	accountSID := os.Getenv("TWILIO_ACCOUNT_SID")
	authToken := os.Getenv("TWILIO_AUTH_TOKEN")
	if accountSID == "" || authToken == "" {
		return fmt.Errorf("missing Twilio credentials: TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN required to download recording")
	}

	mediaURL := recordingURL + ".wav"
	req, err := http.NewRequest("GET", mediaURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request to Twilio recording URL: %v", err)
	}
	req.SetBasicAuth(accountSID, authToken)

	client := &http.Client{}
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

	// Upload using Supabase Storage API
	uploadURL := fmt.Sprintf("%s/storage/v1/object/%s/%s", supabaseURL, bucketName, fileName)

	uploadReq, err := http.NewRequest("POST", uploadURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create upload request: %v", err)
	}

	uploadReq.Header.Set("Authorization", "Bearer "+supabaseKey)
	uploadReq.Header.Set("Content-Type", "audio/wav")
	uploadReq.Header.Set("Cache-Control", "3600")
	uploadReq.Header.Set("x-upsert", "true")

	fmt.Printf("Uploading to bucket: %s, key: %s, URL: %s\n", bucketName, fileName, uploadURL)

	uploadResp, err := client.Do(uploadReq)
	if err != nil {
		return fmt.Errorf("failed to upload to Supabase: %v", err)
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode != http.StatusOK && uploadResp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(uploadResp.Body)
		return fmt.Errorf("upload failed with status %d: %s", uploadResp.StatusCode, string(respBody))
	}

	return nil
}

func twilioAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {

			if !strings.HasPrefix(c.Request().URL.Path, "/twilio/") {
				return next(c)
			}

			authToken := os.Getenv("TWILIO_AUTH_TOKEN")
			if authToken == "" {
				return c.String(http.StatusInternalServerError, "TWILIO_AUTH_TOKEN not configured")
			}

			bodyBytes, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return c.String(http.StatusBadRequest, "Failed to read request body")
			}

			formData, err := url.ParseQuery(string(bodyBytes))
			if err != nil {
				return c.String(http.StatusBadRequest, "Failed to parse form data")
			}

			params := make(map[string]string)
			for key, values := range formData {
				if len(values) > 0 {
					params[key] = values[0]
				}
			}

			signature := c.Request().Header.Get("X-Twilio-Signature")
			requestURL := fmt.Sprintf("https://%s%s", c.Request().Host, c.Request().URL.Path)

			if !validateTwilioSignature(authToken, signature, requestURL, params) {
				return c.String(http.StatusUnauthorized, "Invalid Twilio signature")
			}

			c.Set("twilioParams", params)
			return next(c)
		}
	}
}

func main() {
	e := echo.New()

	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(twilioAuthMiddleware())

	e.GET("/healthz", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.POST("/twilio/voice", func(c echo.Context) error {

		params, ok := c.Get("twilioParams").(map[string]string)
		if !ok {
			return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
		}

		fromNumber := params["From"]
		toNumber := params["To"]

		e.Logger.Infof("Call from %s to %s - starting full call recording", fromNumber, toNumber)

		// Start recording the entire call immediately - recording continues until call ends
		record := &twiml.VoiceRecord{
			MaxLength:                     "3600",        // 1 hour max recording
			PlayBeep:                      "false",       // No beep - silent recording
			Trim:                          "do-not-trim", // Keep full audio even if silent
			RecordingStatusCallback:       "/twilio/recording-status",
			RecordingStatusCallbackMethod: "POST",
			RecordingStatusCallbackEvent:  "completed failed",
		}

		// Welcome message
		welcomeMessage := fmt.Sprintf("Hello! You've reached a secure Twilio webhook. You are calling from %s. This call is being recorded. Press any key to hear a message.", fromNumber)
		say := &twiml.VoiceSay{Message: welcomeMessage}

		// Gather for key input - this handles key presses without stopping the recording
		gather := &twiml.VoiceGather{
			Action:    "/twilio/key-pressed",
			Method:    "POST",
			Timeout:   "30", // Wait 30 seconds for input
			NumDigits: "1",  // Only need one digit
		}

		// If no key is pressed, redirect to wait for more input
		redirect := &twiml.VoiceRedirect{
			Url:    "/twilio/wait-for-input",
			Method: "POST",
		}

		response, err := twiml.Voice([]twiml.Element{record, say, gather, redirect})
		if err != nil {
			return c.String(http.StatusInternalServerError, "failed to build TwiML")
		}
		c.Response().Header().Set(echo.HeaderContentType, "application/xml")
		return c.String(http.StatusOK, response)
	})

	// Handle key press during call - TTS activates but recording continues
	e.POST("/twilio/key-pressed", func(c echo.Context) error {
		params, ok := c.Get("twilioParams").(map[string]string)
		if !ok {
			return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
		}

		digits := params["Digits"]
		fromNumber := params["From"]

		e.Logger.Infof("Key pressed by %s: %s - playing TTS message while recording continues", fromNumber, digits)

		// Play TTS message when key is pressed
		ttsMessage := "Thank you for pressing a key! Your call is still being recorded. You can hang up when you're done, or press another key to hear this message again."
		say := &twiml.VoiceSay{Message: ttsMessage}

		// Continue gathering for more key presses
		gather := &twiml.VoiceGather{
			Action:    "/twilio/key-pressed",
			Method:    "POST",
			Timeout:   "30", // Wait 30 seconds for next input
			NumDigits: "1",  // Only need one digit
		}

		// If no more keys pressed, redirect to wait
		redirect := &twiml.VoiceRedirect{
			Url:    "/twilio/wait-for-input",
			Method: "POST",
		}

		response, err := twiml.Voice([]twiml.Element{say, gather, redirect})
		if err != nil {
			return c.String(http.StatusInternalServerError, "failed to build TwiML")
		}
		c.Response().Header().Set(echo.HeaderContentType, "application/xml")
		return c.String(http.StatusOK, response)
	})

	// Handle timeout when no key is pressed - continue waiting
	e.POST("/twilio/wait-for-input", func(c echo.Context) error {
		params, ok := c.Get("twilioParams").(map[string]string)
		if !ok {
			return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
		}

		fromNumber := params["From"]
		e.Logger.Infof("No key pressed by %s - continuing to wait for input while recording continues", fromNumber)

		// Continue gathering for key presses
		gather := &twiml.VoiceGather{
			Action:    "/twilio/key-pressed",
			Method:    "POST",
			Timeout:   "30", // Wait 30 seconds for input
			NumDigits: "1",  // Only need one digit
		}

		// Keep redirecting back here if no input
		redirect := &twiml.VoiceRedirect{
			Url:    "/twilio/wait-for-input",
			Method: "POST",
		}

		response, err := twiml.Voice([]twiml.Element{gather, redirect})
		if err != nil {
			return c.String(http.StatusInternalServerError, "failed to build TwiML")
		}
		c.Response().Header().Set(echo.HeaderContentType, "application/xml")
		return c.String(http.StatusOK, response)
	})

	e.POST("/twilio/recording-complete", func(c echo.Context) error {
		params, ok := c.Get("twilioParams").(map[string]string)
		if !ok {
			return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
		}

		fromNumber := params["From"]
		recordingURL := params["RecordingUrl"]
		recordingSid := params["RecordingSid"]
		recordingDuration := params["RecordingDuration"]
		digits := params["Digits"]

		e.Logger.Infof("Full call recording completed from %s, URL: %s, Duration: %s seconds, SID: %s", fromNumber, recordingURL, recordingDuration, recordingSid)

		// Log how the recording ended
		if digits == "hangup" {
			e.Logger.Infof("Call recording ended because caller hung up")
		} else if digits != "" {
			e.Logger.Infof("Call recording ended because caller pressed: %s", digits)
		} else {
			e.Logger.Infof("Call recording ended due to timeout or max length")
		}

		// Note: actual media may not be ready yet; we'll upload from the RecordingStatusCallback handler.

		// Thank the caller and end the call
		thankYouMessage := "Thank you for your call. The recording has been saved. Goodbye!"
		say := &twiml.VoiceSay{Message: thankYouMessage}
		hangup := &twiml.VoiceHangup{}
		response, err := twiml.Voice([]twiml.Element{say, hangup})
		if err != nil {
			return c.String(http.StatusInternalServerError, "failed to build TwiML")
		}
		c.Response().Header().Set(echo.HeaderContentType, "application/xml")
		return c.String(http.StatusOK, response)
	})

	// Alternative endpoint for conversation recording (records both parties)
	e.POST("/twilio/voice-conversation", func(c echo.Context) error {
		params, ok := c.Get("twilioParams").(map[string]string)
		if !ok {
			return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
		}

		fromNumber := params["From"]
		toNumber := params["To"]

		e.Logger.Infof("Conversation call from %s to %s", fromNumber, toNumber)

		// Connect to another number with recording enabled
		// Replace with actual destination number
		destinationNumber := os.Getenv("DESTINATION_NUMBER")
		if destinationNumber == "" {
			destinationNumber = "+1234567890" // Fallback - replace with actual number
		}

		// For now, provide instructions on how to use conversation recording
		conversationMessage := fmt.Sprintf("This endpoint demonstrates conversation recording. To use it, set DESTINATION_NUMBER environment variable to %s and implement Dial with record=true", destinationNumber)
		conversationSay := &twiml.VoiceSay{Message: conversationMessage}
		response, err := twiml.Voice([]twiml.Element{conversationSay})
		if err != nil {
			return c.String(http.StatusInternalServerError, "failed to build TwiML")
		}
		c.Response().Header().Set(echo.HeaderContentType, "application/xml")
		return c.String(http.StatusOK, response)
	})

	// Handle what happens after dial completes
	e.POST("/twilio/dial-complete", func(c echo.Context) error {
		params, ok := c.Get("twilioParams").(map[string]string)
		if !ok {
			return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
		}

		dialCallStatus := params["DialCallStatus"]

		var message string
		switch dialCallStatus {
		case "completed":
			message = "Thank you for your call. Goodbye!"
		case "busy":
			message = "The number you're trying to reach is busy. Please try again later."
		case "no-answer":
			message = "The number you're trying to reach is not answering. Please leave a message after the beep."
			// Could add a Record verb here for voicemail
		case "failed":
			message = "We're sorry, but we couldn't connect your call. Please try again later."
		default:
			message = "Thank you for calling. Goodbye!"
		}

		say := &twiml.VoiceSay{Message: message}
		hangup := &twiml.VoiceHangup{}
		response, err := twiml.Voice([]twiml.Element{say, hangup})
		if err != nil {
			return c.String(http.StatusInternalServerError, "failed to build TwiML")
		}
		c.Response().Header().Set(echo.HeaderContentType, "application/xml")
		return c.String(http.StatusOK, response)
	})

	// Recording status callback - called when recording processing is complete
	e.POST("/twilio/recording-status", func(c echo.Context) error {
		params, ok := c.Get("twilioParams").(map[string]string)
		if !ok {
			return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
		}

		callSid := params["CallSid"]
		recordingSid := params["RecordingSid"]
		recordingURL := params["RecordingUrl"]
		recordingStatus := params["RecordingStatus"]
		recordingDuration := params["RecordingDuration"]
		recordingChannels := params["RecordingChannels"]
		recordingSource := params["RecordingSource"]

		e.Logger.Infof("Recording status update: SID=%s, Status=%s, Duration=%s", recordingSid, recordingStatus, recordingDuration)

		switch recordingStatus {
		case "completed":
			e.Logger.Infof("Recording is now available for download: %s", recordingURL)

			// This is the most reliable time to upload the recording
			timestamp := time.Now().Unix()
			fileName := fmt.Sprintf("recording_%s_%d.wav", recordingSid, timestamp)

			go func() {
				err := uploadRecordingToSupabase(recordingURL, fileName)
				if err != nil {
					e.Logger.Errorf("Failed to upload recording from status callback: %v", err)
				} else {
					fmt.Printf("\n✅ RECORDING STATUS: UPLOADED ✅\n")
					fmt.Printf("File: %s\n", fileName)
					fmt.Printf("Call SID: %s\n", callSid)
					fmt.Printf("Recording SID: %s\n", recordingSid)
					fmt.Printf("Duration: %s seconds\n", recordingDuration)
					fmt.Printf("Channels: %s\n", recordingChannels)
					fmt.Printf("Source: %s\n", recordingSource)
					fmt.Printf("=============================\n\n")
					e.Logger.Infof("Recording uploaded successfully via status callback: %s", fileName)
				}
			}()
		case "failed", "absent":
			e.Logger.Errorf("Recording failed or is absent: SID=%s, Status=%s", recordingSid, recordingStatus)
		}

		// Return 200 OK to acknowledge receipt
		return c.String(http.StatusOK, "OK")
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	e.Logger.Fatal(e.Start(":" + port))
}
