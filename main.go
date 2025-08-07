package main

import (
	"net/http"

	"call-demo/config"
	"call-demo/supabase"
	"call-demo/twilio"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	cfg := config.Load()

	storage := supabase.New(supabase.Config{
		URL:            cfg.SupabaseURL,
		ServiceRoleKey: cfg.SupabaseServiceRoleKey,
		Bucket:         cfg.SupabaseBucket,
	})

	twilioService := twilio.New(twilio.Config{
		AccountSID: cfg.TwilioAccountSID,
		AuthToken:  cfg.TwilioAuthToken,
	}, storage)

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	twilioService.RegisterHandlers(e)

	e.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	e.Logger.Fatal(e.Start(":" + cfg.Port))
}
