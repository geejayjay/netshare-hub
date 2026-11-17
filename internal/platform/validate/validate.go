// Package validate collects business-rule validation errors in services.
//
// Shape checks (required, min/max length, enums) are declared as struct tags
// on request types and run automatically before the handler. Use this package
// for the rules tags cannot express ("username is taken", "cannot delete
// yourself"). Both end up in the same response shape.
//
//	v := validate.New()
//	v.Check(len(pw) >= 8, "password", "too_short", "Must be at least 8 characters.")
//	v.Check(!taken, "username", "taken", "This username is already in use.")
//	if err := v.Err(); err != nil { return err }
package validate

import "netshare-hub/internal/platform/errs"

type Errors struct {
	fields []errs.FieldError
}

func New() *Errors { return &Errors{} }

// Check records a field error when ok is false.
func (v *Errors) Check(ok bool, field, code, message string) *Errors {
	if !ok {
		v.Add(field, code, message)
	}
	return v
}

// Add records a field error.
func (v *Errors) Add(field, code, message string) *Errors {
	v.fields = append(v.fields, errs.FieldError{Field: field, Code: code, Message: message})
	return v
}

// Has reports whether the given field already has an error. Use it to skip
// follow-up checks (no point checking uniqueness of a malformed username).
func (v *Errors) Has(field string) bool {
	for _, f := range v.fields {
		if f.Field == field {
			return true
		}
	}
	return false
}

// Err returns nil when everything passed, or one errs.Invalid with all fields.
func (v *Errors) Err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return errs.Invalid(errs.CodeValidationFailed, "Some fields need your attention.", v.fields...)
}
