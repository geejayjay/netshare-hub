package httpx

import (
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2/validation"
)

// requiredMarker lets fieldError recognise "missing required property" errors,
// which name the property inside the message instead of the location.
const requiredMarker = "\x00required:"

// installMessages replaces the framework's technical validation messages
// ("expected length >= 3") with plain ones a user can act on.
//
// Each message is "<code>|<text>". The code goes to FieldError.Code and the
// text to FieldError.Message. Edit this table to reword any message.
func installMessages() {
	validation.MsgExpectedRequiredProperty = "required|" + requiredMarker + "%s"

	validation.MsgExpectedMinLength = "too_short|Must be at least %d characters."
	validation.MsgExpectedMaxLength = "too_long|Must be at most %d characters."
	validation.MsgExpectedMinimumNumber = "too_small|Must be %v or more."
	validation.MsgExpectedMaximumNumber = "too_large|Must be %v or less."
	validation.MsgExpectedExclusiveMinimumNumber = "too_small|Must be more than %v."
	validation.MsgExpectedExclusiveMaximumNumber = "too_large|Must be less than %v."
	validation.MsgExpectedMinItems = "too_few|Must have at least %d items."
	validation.MsgExpectedMaxItems = "too_many|Must have at most %d items."
	validation.MsgExpectedArrayItemsUnique = "not_unique|Items must be unique."

	validation.MsgExpectedOneOf = "invalid_choice|Must be one of: \"%s\"."
	validation.MsgExpectedBePattern = "invalid_format|Must be %s."
	validation.MsgExpectedMatchPattern = "invalid_format|Does not match the required format (%s)."

	validation.MsgExpectedString = "wrong_type|Must be text."
	validation.MsgExpectedInteger = "wrong_type|Must be a whole number."
	validation.MsgExpectedNumber = "wrong_type|Must be a number."
	validation.MsgExpectedBoolean = "wrong_type|Must be true or false."
	validation.MsgExpectedArray = "wrong_type|Must be a list."
	validation.MsgExpectedObject = "wrong_type|Must be an object."

	validation.MsgUnexpectedProperty = "unknown_field|This field is not allowed."
}

// splitMessage separates "<code>|<text>". Messages we did not override get
// the generic code "invalid".
func splitMessage(msg string) (code, text string) {
	if c, t, ok := strings.Cut(msg, "|"); ok && !strings.ContainsAny(c, " .") {
		return c, t
	}
	return "invalid", sentence(msg)
}

var installOnce sync.Once

// Install wires our error format and validation messages into the framework.
// Call it once before creating the API.
func Install() {
	installOnce.Do(func() {
		installErrors()
		installMessages()
	})
}
