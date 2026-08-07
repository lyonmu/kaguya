package config

type DatabaseConfig struct {
	DBType     string `json:"db_type" yaml:"db_type" mapstructure:"db_type"`
	DBPath     string `json:"db_path" yaml:"db_path" mapstructure:"db_path"`
	DBHost     string `json:"db_host" yaml:"db_host" mapstructure:"db_host"`
	DBPort     int    `json:"db_port" yaml:"db_port" mapstructure:"db_port"`
	DBUser     string `json:"db_user" yaml:"db_user" mapstructure:"db_user"`
	DBPassword string `json:"db_password" yaml:"db_password" mapstructure:"db_password"`
	DBName     string `json:"db_name" yaml:"db_name" mapstructure:"db_name"`
}
