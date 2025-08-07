package main

import (
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/twilio/twilio-go/twiml"
)

func main() {
	e := echo.New()

	// Healthcheck
	e.GET("/healthz", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	// Twilio Voice webhook (incoming call)
	e.POST("/twilio/voice", func(c echo.Context) error {
		// Build TwiML: <Say>Hello from your pals at Twilio! Have fun.</Say>
		say := &twiml.VoiceSay{Message: "Hello from your pals at Twilio! Have fun."}
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
