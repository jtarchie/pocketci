package orchestra

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration wraps time.Duration with JSON marshal/unmarshal support.
// It serializes as a human-readable string (e.g., "5m0s") and
// deserializes from either a string ("5m", "10s") or a number (nanoseconds).
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}

	switch value := v.(type) {
	case string:
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return err
		}

		*d = Duration(parsed)
	case float64:
		*d = Duration(time.Duration(value))
	default:
		return fmt.Errorf("invalid duration type %T", v)
	}

	return nil
}

// Std converts the Duration to a standard time.Duration.
func (d Duration) Std() time.Duration {
	return time.Duration(d)
}
