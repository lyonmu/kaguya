package config

import (
	"github.com/lyonmu/gopkg/logger"
	"go.uber.org/zap"
)

// Level is the minimum severity accepted by the logger.
type Level string

const (
	DebugLevel Level = "debug"
	InfoLevel  Level = "info"
	WarnLevel  Level = "warn"
	ErrorLevel Level = "error"
)

// Format controls how log entries are encoded.
type Format string

const (
	ConsoleFormat Format = "console"
	JSONFormat    Format = "json"
)

// LogConfig 包含日志输出及文件轮转配置。
type LogConfig struct {
	Module         string `name:"module" long:"module" env:"LOG_MODULE" help:"日志模块名称" default:"kaguya" mapstructure:"module" yaml:"module" json:"module"`
	Level          Level  `name:"level" long:"level" env:"LOG_LEVEL" help:"日志级别" enum:"debug,info,warn,error" default:"info" mapstructure:"level" yaml:"level" json:"level"`
	Format         Format `name:"format" long:"format" env:"LOG_FORMAT" help:"日志输出格式" enum:"console,json" default:"console" mapstructure:"format" yaml:"format" json:"format"`
	ConsoleEnabled bool   `name:"console-enabled" long:"console-enabled" env:"LOG_CONSOLE_ENABLED" help:"是否输出日志到控制台" default:"true" mapstructure:"console_enabled" yaml:"console_enabled" json:"console_enabled"`
	FileEnabled    bool   `name:"file-enabled" long:"file-enabled" env:"LOG_FILE_ENABLED" help:"是否输出日志到文件" default:"false" mapstructure:"file_enabled" yaml:"file_enabled" json:"file_enabled"`
	FilePath       string `name:"file-path" long:"file-path" env:"LOG_FILE_PATH" help:"日志文件根目录" default:"./logs" mapstructure:"file_path" yaml:"file_path" json:"file_path"`
	MaxSize        int    `name:"max-size" long:"max-size" env:"LOG_MAX_SIZE" help:"单个日志文件最大大小，单位 MB" default:"10" mapstructure:"max_size" yaml:"max_size" json:"max_size"`
	MaxAge         int    `name:"max-age" long:"max-age" env:"LOG_MAX_AGE" help:"日志文件最大保留天数" default:"7" mapstructure:"max_age" yaml:"max_age" json:"max_age"`
	MaxBackups     int    `name:"max-backups" long:"max-backups" env:"LOG_MAX_BACKUPS" help:"日志文件最大备份数量" default:"3" mapstructure:"max_backups" yaml:"max_backups" json:"max_backups"`
	Compress       bool   `name:"compress" long:"compress" env:"LOG_COMPRESS" help:"是否压缩轮转后的日志文件" default:"true" mapstructure:"compress" yaml:"compress" json:"compress"`
	LocalTime      bool   `name:"local-time" long:"local-time" env:"LOG_LOCAL_TIME" help:"日志文件轮转时间是否使用本地时间" default:"true" mapstructure:"local_time" yaml:"local_time" json:"local_time"`
}

// NewLogger 根据应用日志配置创建 Logger。
func (l LogConfig) NewLogger() (*zap.Logger, error) {
	return logger.New(logger.Config{
		Module: l.Module,
		Level:  logger.Level(l.Level),
		Format: logger.Format(l.Format),
		Output: logger.OutputConfig{
			Console: logger.ConsoleOutputConfig{
				Enabled: l.ConsoleEnabled,
			},
			File: logger.FileOutputConfig{
				Enabled:    l.FileEnabled,
				Path:       l.FilePath,
				MaxSize:    l.MaxSize,
				MaxAge:     l.MaxAge,
				MaxBackups: l.MaxBackups,
				Compress:   l.Compress,
				LocalTime:  l.LocalTime,
			},
		},
	})
}
