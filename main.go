package main

import (
	"bytes"
	"context"
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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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
	accessKeyID := os.Getenv("SUPABASE_ACCESS_KEY_ID")
	secretKey := os.Getenv("SUPABASE_SECRET_KEY")
	endpoint := os.Getenv("SUPABASE_S3_ENDPOINT")
	region := os.Getenv("SUPABASE_REGION")
	bucketName := "voice-recording"

	if accessKeyID == "" || secretKey == "" || endpoint == "" {
		return fmt.Errorf("missing Supabase S3 configuration")
	}

	if region == "" {
		region = "us-east-2"
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretKey, "")),
		config.WithRegion(region),
	)
	if err != nil {
		return fmt.Errorf("failed to load config: %v", err)
	}

	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	resp, err := http.Get(recordingURL + ".wav")
	if err != nil {
		return fmt.Errorf("failed to download recording: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read recording: %v", err)
	}

	_, err = s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucketName),
		Key:         aws.String(fileName),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("audio/wav"),
	})

	if err != nil {
		return fmt.Errorf("failed to upload to Supabase: %v", err)
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

		e.Logger.Infof("Call from %s to %s", fromNumber, toNumber)

		record := &twiml.VoiceRecord{
			Action:      "/twilio/recording-complete",
			Method:      "POST",
			Timeout:     "5",
			MaxLength:   "5",
			FinishOnKey: "",
			PlayBeep:    "false",
		}

		response, err := twiml.Voice([]twiml.Element{record})
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

		e.Logger.Infof("Recording completed from %s, URL: %s", fromNumber, recordingURL)

		timestamp := time.Now().Unix()
		fileName := fmt.Sprintf("recording_%s_%d.wav", recordingSid, timestamp)

		go func() {
			err := uploadRecordingToSupabase(recordingURL, fileName)
			if err != nil {
				e.Logger.Errorf("Failed to upload recording: %v", err)
			} else {
				fmt.Printf("\n📁 RECORDING UPLOADED 📁\n")
				fmt.Printf("File: %s\n", fileName)
				fmt.Printf("From: %s\n", fromNumber)
				fmt.Printf("Recording SID: %s\n", recordingSid)
				fmt.Printf("========================\n\n")
				e.Logger.Infof("Recording uploaded successfully: %s", fileName)
			}
		}()

		message := fmt.Sprintf("Hello! You've reached a secure Twilio webhook. You are calling from %s. Welcome!", fromNumber)
		say := &twiml.VoiceSay{Message: message}
		response, err := twiml.Voice([]twiml.Element{say})
		if err != nil {
			return c.String(http.StatusInternalServerError, "failed to build TwiML")
		}
		c.Response().Header().Set(echo.HeaderContentType, "application/xml")
		return c.String(http.StatusOK, response)
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	e.Logger.Fatal(e.Start(":" + port))
}
