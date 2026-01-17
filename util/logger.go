package util

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

var Logger = NewLogger()

func NewLogger() *zerolog.Logger {
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	logger := zerolog.New(output).With().Timestamp().Logger()
	return &logger
}
