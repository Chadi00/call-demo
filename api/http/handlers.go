package http

import (
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/twilio/twilio-go/twiml"

	svc "call-demo/internal/usecase"
)

type Handlers struct {
	Twilio svc.TwilioService
}

func NewHandlers(twilioService svc.TwilioService) Handlers {
	return Handlers{Twilio: twilioService}
}

func (h Handlers) Register(e *echo.Echo) {
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.POST("/twilio/voice", h.voice)
	e.POST("/twilio/key-pressed", h.keyPressed)
	e.POST("/twilio/wait-for-input", h.waitForInput)
	e.POST("/twilio/recording-complete", h.recordingComplete)
	e.POST("/twilio/recording-status", h.recordingStatus)
	e.POST("/twilio/voice-conversation", h.voiceConversation)
	e.POST("/twilio/dial-complete", h.dialComplete)
}

func (h Handlers) voice(c echo.Context) error {
	params, ok := c.Get("twilioParams").(map[string]string)
	if !ok {
		return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
	}

	fromNumber := params["From"]
	toNumber := params["To"]
	c.Echo().Logger.Infof("Call from %s to %s - preparing to start full call recording", fromNumber, toNumber)

	callSid := params["CallSid"]
	if callSid != "" {
		absoluteCallback := h.Twilio.BuildAbsoluteURL(c, "/twilio/recording-status")
		go func() {
			if err := h.Twilio.StartCallRecording(callSid, absoluteCallback); err != nil {
				c.Echo().Logger.Errorf("Failed to start call-level recording for CallSid=%s: %v", callSid, err)
			} else {
				c.Echo().Logger.Infof("Started continuous recording for CallSid=%s", callSid)
			}
		}()
	} else {
		c.Echo().Logger.Warn("CallSid not present in params; cannot start call-level recording")
	}

	welcomeMessage := fmt.Sprintf("Hello! You've reached a secure Twilio webhook. You are calling from %s. This call is being recorded. Press any key to hear a message.", fromNumber)
	twimlResponse := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
        <Response>
          <Say>%s</Say>
          <Gather action="/twilio/key-pressed" method="POST" timeout="30" numDigits="1" />
          <Redirect method="POST">/twilio/wait-for-input</Redirect>
        </Response>`, welcomeMessage)

	c.Response().Header().Set(echo.HeaderContentType, "application/xml")
	return c.String(http.StatusOK, twimlResponse)
}

func (h Handlers) keyPressed(c echo.Context) error {
	params, ok := c.Get("twilioParams").(map[string]string)
	if !ok {
		return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
	}

	digits := params["Digits"]
	fromNumber := params["From"]
	c.Echo().Logger.Infof("Key pressed by %s: %s - playing TTS message while recording continues", fromNumber, digits)

	ttsMessage := "Thank you for pressing a key! Your call is still being recorded. You can hang up when you're done, or press another key to hear this message again."
	say := &twiml.VoiceSay{Message: ttsMessage}

	gather := &twiml.VoiceGather{Action: "/twilio/key-pressed", Method: "POST", Timeout: "30", NumDigits: "1"}
	redirect := &twiml.VoiceRedirect{Url: "/twilio/wait-for-input", Method: "POST"}
	response, err := twiml.Voice([]twiml.Element{say, gather, redirect})
	if err != nil {
		return c.String(http.StatusInternalServerError, "failed to build TwiML")
	}
	c.Response().Header().Set(echo.HeaderContentType, "application/xml")
	return c.String(http.StatusOK, response)
}

func (h Handlers) waitForInput(c echo.Context) error {
	params, ok := c.Get("twilioParams").(map[string]string)
	if !ok {
		return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
	}
	fromNumber := params["From"]
	c.Echo().Logger.Infof("No key pressed by %s - continuing to wait for input while recording continues", fromNumber)

	gather := &twiml.VoiceGather{Action: "/twilio/key-pressed", Method: "POST", Timeout: "30", NumDigits: "1"}
	redirect := &twiml.VoiceRedirect{Url: "/twilio/wait-for-input", Method: "POST"}
	response, err := twiml.Voice([]twiml.Element{gather, redirect})
	if err != nil {
		return c.String(http.StatusInternalServerError, "failed to build TwiML")
	}
	c.Response().Header().Set(echo.HeaderContentType, "application/xml")
	return c.String(http.StatusOK, response)
}

func (h Handlers) recordingComplete(c echo.Context) error {
	params, ok := c.Get("twilioParams").(map[string]string)
	if !ok {
		return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
	}
	fromNumber := params["From"]
	recordingURL := params["RecordingUrl"]
	recordingSid := params["RecordingSid"]
	recordingDuration := params["RecordingDuration"]
	digits := params["Digits"]

	c.Echo().Logger.Infof("Full call recording completed from %s, URL: %s, Duration: %s seconds, SID: %s", fromNumber, recordingURL, recordingDuration, recordingSid)
	if digits == "hangup" {
		c.Echo().Logger.Infof("Call recording ended because caller hung up")
	} else if digits != "" {
		c.Echo().Logger.Infof("Call recording ended because caller pressed: %s", digits)
	} else {
		c.Echo().Logger.Infof("Call recording ended due to timeout or max length")
	}

	thankYouMessage := "Thank you for your call. The recording has been saved. Goodbye!"
	say := &twiml.VoiceSay{Message: thankYouMessage}
	hangup := &twiml.VoiceHangup{}
	response, err := twiml.Voice([]twiml.Element{say, hangup})
	if err != nil {
		return c.String(http.StatusInternalServerError, "failed to build TwiML")
	}
	c.Response().Header().Set(echo.HeaderContentType, "application/xml")
	return c.String(http.StatusOK, response)
}

func (h Handlers) voiceConversation(c echo.Context) error {
	params, ok := c.Get("twilioParams").(map[string]string)
	if !ok {
		return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
	}
	fromNumber := params["From"]
	toNumber := params["To"]
	_ = toNumber
	c.Echo().Logger.Infof("Conversation call from %s to %s", fromNumber, toNumber)

	destinationNumber := h.Twilio.DestinationNumber()
	if destinationNumber == "" {
		destinationNumber = "+1234567890"
	}
	conversationMessage := fmt.Sprintf("This endpoint demonstrates conversation recording. To use it, set DESTINATION_NUMBER environment variable to %s and implement Dial with record=true", destinationNumber)
	conversationSay := &twiml.VoiceSay{Message: conversationMessage}
	response, err := twiml.Voice([]twiml.Element{conversationSay})
	if err != nil {
		return c.String(http.StatusInternalServerError, "failed to build TwiML")
	}
	c.Response().Header().Set(echo.HeaderContentType, "application/xml")
	return c.String(http.StatusOK, response)
}

func (h Handlers) dialComplete(c echo.Context) error {
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
}

func (h Handlers) recordingStatus(c echo.Context) error {
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

	c.Echo().Logger.Infof("Recording status update: SID=%s, Status=%s, Duration=%s", recordingSid, recordingStatus, recordingDuration)

	switch recordingStatus {
	case "completed":
		c.Echo().Logger.Infof("Recording is now available for download: %s", recordingURL)
		timestamp := time.Now().Unix()
		fileName := fmt.Sprintf("recording_%s_%d.wav", recordingSid, timestamp)
		go func() {
			if err := h.Twilio.UploadRecordingToStorage(recordingURL, fileName); err != nil {
				c.Echo().Logger.Errorf("Failed to upload recording from status callback: %v", err)
			} else {
				fmt.Printf("\n✅ RECORDING STATUS: UPLOADED ✅\n")
				fmt.Printf("File: %s\n", fileName)
				fmt.Printf("Call SID: %s\n", callSid)
				fmt.Printf("Recording SID: %s\n", recordingSid)
				fmt.Printf("Duration: %s seconds\n", recordingDuration)
				fmt.Printf("Channels: %s\n", recordingChannels)
				fmt.Printf("Source: %s\n", recordingSource)
				fmt.Printf("=============================\n\n")
				c.Echo().Logger.Infof("Recording uploaded successfully via status callback: %s", fileName)
			}
		}()
	case "failed", "absent":
		c.Echo().Logger.Errorf("Recording failed or is absent: SID=%s, Status=%s", recordingSid, recordingStatus)
	}

	return c.String(http.StatusOK, "OK")
}
