package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/lionelchamorro/orquestalite/internal/activity"
)

func strictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func contractError(op string, err error) error {
	return &activity.Error{Class: activity.ErrorInvalidContract, Op: op, Err: err}
}
