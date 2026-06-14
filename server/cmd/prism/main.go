package main

import (
	"log"

	"github.com/jushjay/prism/internal/app"
)

func main() {
	server, err := app.NewServer()
	if err != nil {
		log.Fatalf("failed to initialize server: %v", err)
	}

	if err := server.Run(); err != nil {
		log.Fatalf("server exited with error: %v", err)
	}
}
