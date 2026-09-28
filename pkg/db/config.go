package db

type Config struct {
	// Dir is a directory to keep database files
	Dir    string        `toml:"dir" doc:"Database directory. Defaults to a \"db\" directory next to the configuration file."`
	Badger *BadgerConfig `toml:"badger" doc:"Advanced BadgerDB tuning; usually not needed."`
}
