package logging

import (
	"fmt"

	"go.uber.org/zap"
)

// New constructs a zap logger using a production configuration tailored for the
// service. Supported levels: debug, info, warn, error. Defaults to info.
func New(level string) (*zap.Logger, error) {
	config := zap.NewProductionConfig()
	config.Encoding = "json"
	if err := config.Level.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q: %w", level, err)
	}
	config.EncoderConfig.TimeKey = "ts"
	config.EncoderConfig.MessageKey = "msg"
	config.EncoderConfig.LevelKey = "level"
	config.EncoderConfig.CallerKey = "caller"
	config.EncoderConfig.StacktraceKey = "stack"

	logger, err := config.Build()
	if err != nil {
		return nil, fmt.Errorf("build logger: %w", err)
	}
	return logger, nil
}
