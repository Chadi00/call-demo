package main

import (
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

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/twilio/twilio-go/twiml"
)

// getAllowedNumbers returns the list of allowed phone numbers from environment
func getAllowedNumbers() map[string]bool {
	allowedStr := os.Getenv("ALLOWED_NUMBERS")
	if allowedStr == "" {
		return map[string]bool{}
	}

	allowed := make(map[string]bool)
	numbers := strings.Split(allowedStr, ",")
	for _, number := range numbers {
		// Normalize phone number (remove spaces, dashes, etc.)
		normalized := strings.ReplaceAll(strings.TrimSpace(number), " ", "")
		normalized = strings.ReplaceAll(normalized, "-", "")
		normalized = strings.ReplaceAll(normalized, "(", "")
		normalized = strings.ReplaceAll(normalized, ")", "")
		if normalized != "" {
			allowed[normalized] = true
		}
	}
	return allowed
}

// validateTwilioSignature validates that the request came from Twilio
func validateTwilioSignature(authToken, signature, url string, params map[string]string) bool {
	if authToken == "" || signature == "" {
		return false
	}

	// Create the expected signature
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

// twilioAuthMiddleware validates Twilio requests and allowed phone numbers
func twilioAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Skip validation for non-Twilio endpoints
			if !strings.HasPrefix(c.Request().URL.Path, "/twilio/") {
				return next(c)
			}

			authToken := os.Getenv("TWILIO_AUTH_TOKEN")
			if authToken == "" {
				return c.String(http.StatusInternalServerError, "TWILIO_AUTH_TOKEN not configured")
			}

			// Read the request body
			bodyBytes, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return c.String(http.StatusBadRequest, "Failed to read request body")
			}

			// Parse form data
			formData, err := url.ParseQuery(string(bodyBytes))
			if err != nil {
				return c.String(http.StatusBadRequest, "Failed to parse form data")
			}

			// Convert to map for signature validation
			params := make(map[string]string)
			for key, values := range formData {
				if len(values) > 0 {
					params[key] = values[0]
				}
			}

			// Validate Twilio signature
			signature := c.Request().Header.Get("X-Twilio-Signature")
			requestURL := fmt.Sprintf("https://%s%s", c.Request().Host, c.Request().URL.Path)

			if !validateTwilioSignature(authToken, signature, requestURL, params) {
				return c.String(http.StatusUnauthorized, "Invalid Twilio signature")
			}

			// Check if phone number is allowed
			fromNumber := params["From"]
			if fromNumber == "" {
				return c.String(http.StatusBadRequest, "Missing From parameter")
			}

			allowedNumbers := getAllowedNumbers()
			if len(allowedNumbers) > 0 {
				// Normalize the incoming number for comparison
				normalizedFrom := strings.ReplaceAll(fromNumber, "+", "")
				normalizedFrom = strings.ReplaceAll(normalizedFrom, " ", "")
				normalizedFrom = strings.ReplaceAll(normalizedFrom, "-", "")
				normalizedFrom = strings.ReplaceAll(normalizedFrom, "(", "")
				normalizedFrom = strings.ReplaceAll(normalizedFrom, ")", "")

				if !allowedNumbers[normalizedFrom] && !allowedNumbers[fromNumber] {
					return c.String(http.StatusForbidden, "Phone number not authorized")
				}
			}

			// Store parsed form data in context for use in handlers
			c.Set("twilioParams", params)
			return next(c)
		}
	}
}

func main() {
	e := echo.New()

	// Add middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(twilioAuthMiddleware())

	// Healthcheck
	e.GET("/healthz", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Twilio Voice webhook (incoming call)
	e.POST("/twilio/voice", func(c echo.Context) error {
		// Get Twilio parameters from middleware
		params, ok := c.Get("twilioParams").(map[string]string)
		if !ok {
			return c.String(http.StatusInternalServerError, "Failed to get Twilio parameters")
		}

		fromNumber := params["From"]
		toNumber := params["To"]

		// Log the authorized call
		e.Logger.Infof("Authorized call from %s to %s", fromNumber, toNumber)

		// Build TwiML response
		message := fmt.Sprintf("Hello! This is a secure webhook. You are calling from %s.", fromNumber)
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
