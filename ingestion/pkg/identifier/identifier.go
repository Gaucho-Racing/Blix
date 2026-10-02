package identifier

import ulid "github.com/gaucho-racing/ulid-go"

type Prefix string

const (
	Worker   Prefix = "worker"
	Document Prefix = "document"
)

func New(prefix Prefix) string {
	return ulid.Make().Prefixed(string(prefix))
}

func IsValid(value string, prefix Prefix) bool {
	parsedPrefix, parsed, err := ulid.ParsePrefixed(value)
	return err == nil && parsedPrefix == string(prefix) && parsed.Prefixed(parsedPrefix) == value
}
