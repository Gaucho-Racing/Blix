package identifier

import ulid "github.com/gaucho-racing/ulid-go"

func New() string {
	return ulid.Make().String()
}
