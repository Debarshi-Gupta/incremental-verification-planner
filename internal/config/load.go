package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

var ErrInvalidConfig = errors.New("invalid IVP configuration")

func LoadFile(filename string) (Config, error) {
	cfg := Defaults()
	b, err := os.ReadFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, Validate(cfg)
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	cfg, err = Decode(bytes.NewReader(b), cfg)
	if err != nil {
		return cfg, err
	}
	if err := Validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Decode overlays strict YAML onto base. Unknown and duplicate mapping keys,
// multiple documents, and trailing data are errors.
func Decode(r io.Reader, base Config) (Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	if err := dec.Decode(&base); err != nil {
		return base, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return base, fmt.Errorf("%w: multiple YAML documents are forbidden", ErrInvalidConfig)
		}
		return base, fmt.Errorf("%w: trailing YAML: %v", ErrInvalidConfig, err)
	}
	if err := Validate(base); err != nil {
		return base, err
	}
	return base, nil
}
