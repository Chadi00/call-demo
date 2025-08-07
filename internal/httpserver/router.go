package httpserver

import (
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// New creates a configured Echo server instance.
func New() *echo.Echo {
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	return e
}
