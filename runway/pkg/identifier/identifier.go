package identifier

import ulid "github.com/gaucho-racing/ulid-go"

func New() string {
	return ulid.Make().String()
}

func IsValid(value string) bool {
	_, err := ulid.ParseStrict(value)
	return err == nil
}
