package service

import (
	"errors"

	"gorm.io/gorm"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrUnavailable  = errors.New("service unavailable")
	ErrInvalid      = errors.New("invalid request")
	ErrNotFound     = errors.New("resource not found")
	ErrConflict     = errors.New("resource state conflict")
)

func mapDatabaseError(err error) error {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return ErrConflict
	default:
		return err
	}
}
