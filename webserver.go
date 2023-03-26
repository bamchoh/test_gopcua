package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type ErrorResponse struct {
	Message string `json:"message"`
}

type WebServer struct {
	db Dao
}

func (srv *WebServer) getApiAlarms(c echo.Context) error {
	c.Response().Header().Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.Response().WriteHeader(http.StatusOK)

	enc := json.NewEncoder(c.Response())
	for {
		alarms, err := srv.db.Alarms()
		if err != nil {
			c.JSON(http.StatusOK, ErrorResponse{err.Error()})
		}

		if err := enc.Encode(alarms); err != nil {
			c.JSON(http.StatusOK, ErrorResponse{err.Error()})
		}

		c.Response().Flush()
		time.Sleep(100 * time.Millisecond)
	}
}

func (srv *WebServer) Start(addr string, ctx context.Context) {
	e := echo.New()

	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())

	e.Static("/", "web/build")

	e.File("/", "web/build/index.html")

	e.GET("/api/alarms", srv.getApiAlarms)

	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			e.Logger.Fatal("shutting down the server")
		}
	}()

	<-ctx.Done()

	fmt.Println("web server: other goroutine was ended")

	ctx2, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.Shutdown(ctx2); err != nil {
		e.Logger.Fatal(err)
	}
}
