package main

import (
	"net/http"

	"github.com/xfey/coconet/packages/contracts"
)

func unsupported(code, message string) error {
	return &authorityError{HTTPStatus: http.StatusUnprocessableEntity, Status: contracts.StatusUnsupported, Code: code, Message: message}
}
