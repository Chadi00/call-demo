package middleware

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"
)

// validateTwilioSignature verifies Twilio request signatures.
func validateTwilioSignature(authToken, signature, fullURL string, params map[string]string) bool {
	if authToken == "" || signature == "" {
		return false
	}

	data := fullURL
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

// TwilioAuth validates Twilio webhook requests using the signature header.
func TwilioAuth(getAuthToken func() string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !strings.HasPrefix(c.Request().URL.Path, "/twilio/") {
				return next(c)
			}

			authToken := getAuthToken()
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
