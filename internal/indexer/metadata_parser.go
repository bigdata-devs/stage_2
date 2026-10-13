package indexer

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"boogle-engine/internal/models"
)

// UnknownValue replaces any metadata field that the header does not provide.
const UnknownValue = "Unknown"

var (
	fieldLinePattern         = regexp.MustCompile(`^([A-Za-z][A-Za-z ]*?):[ \t]*(.*)$`)
	languageSeparatorPattern = regexp.MustCompile(`\s*,\s*|\s+and\s+|\s*;\s*`)
	bracketSegmentPattern    = regexp.MustCompile(`\[[^\]]*\]`)
	yearPattern              = regexp.MustCompile(`\d{4}`)
)

// languageCodes maps Gutenberg language names to ISO 639 codes (Stage 1 LANGUAGE_CODES).
var languageCodes = map[string]string{
	"afrikaans": "af", "arabic": "ar", "bulgarian": "bg", "catalan": "ca", "chinese": "zh",
	"czech": "cs", "danish": "da", "dutch": "nl", "english": "en", "esperanto": "eo",
	"finnish": "fi", "french": "fr", "galician": "gl", "german": "de", "greek": "el",
	"greek, ancient (to 1453)": "grc", "hebrew": "he", "hungarian": "hu", "icelandic": "is",
	"irish": "ga", "italian": "it", "japanese": "ja", "korean": "ko", "latin": "la",
	"middle english (1100-1500)": "enm", "norwegian": "no", "old english (ca. 450-1100)": "ang",
	"polish": "pl", "portuguese": "pt", "romanian": "ro", "russian": "ru", "serbian": "sr",
	"spanish": "es", "swedish": "sv", "tagalog": "tl", "welsh": "cy",
}

// headerFields maps each lower-cased field name ("title", "author"...) to its values in order.
type headerFields map[string][]string

// ExtractMetadata ports extract_metadata: it returns the title, the authors joined with "; ",
// the language codes, the original publication year and the Gutenberg release year of a header.
// Missing text fields become UnknownValue and missing years become 0.
func ExtractMetadata(bookID models.BookID, header string) models.BookMetadata {
	fields := parseHeaderFields(header)
	return models.BookMetadata{
		BookID:          bookID,
		Title:           firstValueOrUnknown(fields["title"]),
		Author:          joinedValuesOrUnknown(fields["author"]),
		Language:        NormalizeLanguage(strings.Join(fields["language"], ", ")),
		PublicationYear: firstYear(fields["original publication"]),
		ReleaseYear:     firstYear(fields["release date"]),
	}
}

// NormalizeLanguage maps language names to ISO 639 codes: "English" -> "en" and
// "English, French" -> "en,fr". Names without a known code are kept in lower case.
func NormalizeLanguage(rawLanguage string) string {
	cleaned := strings.ToLower(collapseWhitespace(rawLanguage))
	if cleaned == "" {
		return UnknownValue
	}
	if code, known := languageCodes[cleaned]; known {
		return code
	}
	return strings.Join(distinctLanguageCodes(languageSeparatorPattern.Split(cleaned, -1)), ",")
}

// parseHeaderFields reads "Name: value" lines. Indented lines continue the previous value, and
// a field such as Author may appear several times.
func parseHeaderFields(header string) headerFields {
	fields := headerFields{}
	currentField := ""
	for _, line := range strings.Split(strings.ReplaceAll(header, "\r", ""), "\n") {
		currentField = fields.consumeLine(line, currentField)
	}
	fields.collapseValues()
	return fields
}

// consumeLine records one header line and returns the field that a following indented line extends.
func (fields headerFields) consumeLine(line string, currentField string) string {
	if strings.TrimSpace(line) == "" {
		return ""
	}
	if startsWithWhitespace(line) {
		fields.extendLastValue(currentField, line)
		return currentField
	}
	match := fieldLinePattern.FindStringSubmatch(strings.TrimSpace(line))
	if match == nil {
		return ""
	}
	fieldName := strings.ToLower(strings.TrimSpace(match[1]))
	fields[fieldName] = append(fields[fieldName], match[2])
	return fieldName
}

func (fields headerFields) extendLastValue(fieldName string, continuationLine string) {
	values := fields[fieldName]
	if len(values) == 0 {
		return
	}
	values[len(values)-1] += " " + strings.TrimSpace(continuationLine)
}

func (fields headerFields) collapseValues() {
	for _, values := range fields {
		for position, value := range values {
			values[position] = collapseWhitespace(value)
		}
	}
}

func firstValueOrUnknown(values []string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return UnknownValue
}

func joinedValuesOrUnknown(values []string) string {
	joined := strings.Join(values, "; ")
	if joined == "" {
		return UnknownValue
	}
	return joined
}

// firstYear returns the first four-digit year of the first value that contains one,
// ignoring bracketed segments such as "[eBook #2147]". A missing or unreadable year
// yields 0, which models.BookMetadata documents as "unknown".
func firstYear(values []string) int {
	for _, value := range values {
		if year := yearIn(bracketSegmentPattern.ReplaceAllString(value, " ")); year != 0 {
			return year
		}
	}
	return 0
}

func yearIn(text string) int {
	match := yearPattern.FindString(text)
	if match == "" {
		return 0
	}
	year, err := strconv.Atoi(match)
	if err != nil || year == 0 {
		return 0
	}
	return year
}

func distinctLanguageCodes(languageNames []string) []string {
	codes := []string{}
	seen := map[string]bool{}
	for _, languageName := range languageNames {
		code := languageCode(languageName)
		if code != "" && !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	return codes
}

// languageCode returns the code of a language name; values that already look like codes are kept.
func languageCode(languageName string) string {
	if code, known := languageCodes[languageName]; known {
		return code
	}
	return languageName
}

func startsWithWhitespace(line string) bool {
	firstCharacter, _ := utf8.DecodeRuneInString(line)
	return unicode.IsSpace(firstCharacter)
}

// collapseWhitespace trims the value and turns every internal run of whitespace into one space.
func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
