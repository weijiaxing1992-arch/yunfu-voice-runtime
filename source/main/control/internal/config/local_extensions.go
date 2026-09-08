package config

import "errors"

// validateLocalExtensions 只允许有限、精确号码，不把正则或FreeSWITCH XML当作已实现的拨号计划。
func validateLocalExtensions(values []string) error {
	if len(values) > 256 {
		return errors.New("local_extensions cannot exceed 256 entries")
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if len(value) < 1 || len(value) > 32 || seen[value] {
			return errors.New("local_extensions must be unique 1..32 character numbers")
		}
		for _, character := range value {
			if (character < '0' || character > '9') && character != '+' {
				return errors.New("local_extensions accepts only digits and +")
			}
		}
		seen[value] = true
	}
	return nil
}
