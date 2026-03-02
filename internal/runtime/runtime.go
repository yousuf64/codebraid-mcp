package runtime

// Language represents a supported execution language
type Language string

const (
	// LanguageTypeScript represents TypeScript/JavaScript execution
	LanguageTypeScript Language = "typescript"

	// LanguagePython represents Python execution
	LanguagePython Language = "python"
)

// IsSupported returns true if the language is supported
func (l Language) IsSupported() bool {
	return l == LanguageTypeScript || l == LanguagePython
}

// String returns the string representation of the language
func (l Language) String() string {
	return string(l)
}

// ParseLanguage parses a language string into a Language type
func ParseLanguage(s string) (Language, bool) {
	lang := Language(s)
	return lang, lang.IsSupported()
}

// GetDefault returns the default language
func GetDefault() Language {
	return LanguageTypeScript
}

// GetFileExtension returns the file extension for a language
func (l Language) GetFileExtension() string {
	switch l {
	case LanguageTypeScript:
		return ".ts"
	case LanguagePython:
		return ".py"
	default:
		return ".txt"
	}
}

// GetModulePath returns the module path prefix for a language
// TypeScript: /servers/<name>/
// Python: /servers/<name>/
func (l Language) GetModulePath() string {
	switch l {
	case LanguageTypeScript:
		return "/servers"
	case LanguagePython:
		return "/servers"
	default:
		return "/servers"
	}
}

// GetWorkspacePath returns the workspace path (same for all languages)
func GetWorkspacePath() string {
	return "/workspace"
}
