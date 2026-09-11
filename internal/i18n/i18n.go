// Package i18n localizes application messages at presentation boundaries.
// Fixed source messages are catalog keys (as in gettext); user data is never
// searched or replaced. Language is scoped to an invocation, not global state.
package i18n

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Language string

const (
	Chinese Language = "zh"
	English Language = "en"
)

//go:embed en.json
var englishJSON []byte

var english = loadCatalog(englishJSON)

func loadCatalog(data []byte) map[string]string {
	var catalog map[string]string
	if err := json.Unmarshal(data, &catalog); err != nil {
		panic(err)
	}
	return catalog
}

func Parse(value string) (Language, error) {
	switch Language(strings.ToLower(strings.TrimSpace(value))) {
	case Chinese, English:
		return Language(strings.ToLower(strings.TrimSpace(value))), nil
	default:
		return "", Errorf("语言必须是 zh 或 en")
	}
}

// FromArgs returns the last valid explicit --lang value. Invalid values are
// left to the CLI parser so startup can still report them as normal arguments.
func FromArgs(args []string) (Language, bool) {
	var language Language
	found := false
	for index := 0; index < len(args); index++ {
		if args[index] == "--" {
			break
		}
		value := ""
		switch {
		case args[index] == "--lang" && index+1 < len(args):
			index++
			value = args[index]
		case strings.HasPrefix(args[index], "--lang="):
			value = strings.TrimPrefix(args[index], "--lang=")
		default:
			continue
		}
		parsed, err := Parse(value)
		if err == nil {
			language, found = parsed, true
		}
	}
	return language, found
}

type languageKey struct{}
type preferencesKey struct{}

func WithLanguage(ctx context.Context, language Language) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, languageKey{}, language)
}

func FromContext(ctx context.Context) Language {
	if ctx != nil {
		if language, ok := ctx.Value(languageKey{}).(Language); ok && language == English {
			return English
		}
	}
	return Chinese
}

type Preferences interface {
	LoadLanguage() (Language, error)
	SaveLanguage(Language) error
}

func WithPreferences(ctx context.Context, preferences Preferences) context.Context {
	return context.WithValue(ctx, preferencesKey{}, preferences)
}

func PreferencesFromContext(ctx context.Context) Preferences {
	if ctx == nil {
		return nil
	}
	preferences, _ := ctx.Value(preferencesKey{}).(Preferences)
	return preferences
}

func T(language Language, key string, args ...any) string {
	format := key
	if language == English {
		if translated, ok := english[key]; ok {
			format = translated
		}
	}
	if len(args) == 0 {
		return format
	}
	localized := make([]any, len(args))
	for index, arg := range args {
		switch value := arg.(type) {
		case error:
			localized[index] = errors.New(Error(language, value))
		case Message:
			localized[index] = value.Text(language)
		default:
			localized[index] = arg
		}
	}
	return fmt.Sprintf(strings.ReplaceAll(format, "%w", "%v"), localized...)
}

// Message retains parameters so a pending notification can be re-rendered.
type Message struct {
	Key  string
	Args []any
}

func M(key string, args ...any) Message         { return Message{Key: key, Args: args} }
func (m Message) Text(language Language) string { return T(language, m.Key, m.Args...) }
func (m Message) String() string                { return m.Text(Chinese) }

type messageError struct {
	message  Message
	original error
}

func Errorf(key string, args ...any) error {
	return &messageError{message: M(key, args...), original: fmt.Errorf(key, args...)}
}

func (e *messageError) Error() string                      { return e.original.Error() }
func (e *messageError) Unwrap() error                      { return e.original }
func (e *messageError) Localized(language Language) string { return e.message.Text(language) }

// Error translates only explicitly localizable errors. Foreign diagnostics and
// arbitrary strings remain untouched, including strings containing credentials.
func Error(language Language, err error) string {
	if err == nil {
		return ""
	}
	if localized, ok := err.(interface{ Localized(Language) string }); ok {
		return localized.Localized(language)
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		parts := make([]string, 0, len(many.Unwrap()))
		for _, child := range many.Unwrap() {
			if child != nil {
				parts = append(parts, Error(language, child))
			}
		}
		return strings.Join(parts, "\n")
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if child := wrapped.Unwrap(); child != nil && err.Error() == child.Error() {
			return Error(language, child)
		}
	}
	return err.Error()
}
