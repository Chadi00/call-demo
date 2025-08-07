package main

import (
	"log"

	apihttp "call-demo/api/http"
	"call-demo/internal/config"
	"call-demo/internal/httpserver"
	"call-demo/internal/infra/storage"
	mw "call-demo/internal/middleware"
	"call-demo/internal/usecase"
)

func main() {
	cfg := config.Load()

	e := httpserver.New()

	// Middleware: Twilio signature auth
	e.Use(mw.TwilioAuth(func() string { return cfg.TwilioAuthToken }))

	// Infra: storage
	supa := storage.NewSupabaseStorage(cfg.SupabaseURL, cfg.SupabaseServiceRoleKey, cfg.SupabaseBucket)

	// Use cases / services
	twilioSvc := usecase.NewTwilioService(cfg.TwilioAccountSID, cfg.TwilioAuthToken, cfg.DestinationNumber, supa)

	// HTTP handlers
	handlers := apihttp.NewHandlers(twilioSvc)
	handlers.Register(e)

	if err := e.Start(":" + cfg.Port); err != nil {
		log.Fatal(err)
	}
}
