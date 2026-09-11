package builtin

import (
	"errors"

	"github.com/collectiveai-team/orquesta-lite/internal/activity"
)

func asActivityError(err error, target **activity.Error) bool {
	return errors.As(err, target)
}
