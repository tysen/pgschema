package main

import (
	"github.com/joho/godotenv"
	"github.com/tysen/pgschema/cmd"
)

func main() {
	// Load .env file if it exists (silently ignore errors)
	_ = godotenv.Load()

	cmd.Execute()
}
