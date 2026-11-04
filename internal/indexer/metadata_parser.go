package indexer

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"boogle-engine/internal/models"
)

const UnknownValue = "Unknown"

var (
	fieldLinePattern         = regexp.MustCompile(`^([A-Za-z][A-Za-z ]*?):[ \t]*(.*)$`)
	languageSeparatorPattern = regexp.MustCompile(`\s*,\s*|\s+and\s+|\s*;\s*`)
	bracketSegmentPattern    = regexp.MustCompile(`\[[^\]]*\]`)
	yearPattern              = regexp.MustCompile(`\d{4}`)
)

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

type headerFields map[string][]string

func ExtractMetadata(bookID models.BookID, header string) models.BookMetadata {
	fields := parseHeaderFields(header)
	releaseYear := firstYear(fields["release date"])
	return models.BookMetadata{
		BookID:          bookID,
		Title:           firstValueOrUnknown(fields["title"]),
		Author:          joinedValuesOrUnknown(fields["author"]),
		Language:        NormalizeLanguage(strings.Join(fields["language"], ", ")),
		PublicationYear: publicationYear(fields["original publication"], releaseYear),
		ReleaseYear:     releaseYear,
	}
}

func publicationYear(originalPublication []string, releaseYear int) int {
	if year := firstYear(originalPublication); year != 0 {
		return year
	}
	return releaseYear
}

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

func parseHeaderFields(header string) headerFields {
	fields := headerFields{}
	currentField := ""
	for _, line := range strings.Split(strings.ReplaceAll(header, "\r", ""), "\n") {
		currentField = fields.consumeLine(line, currentField)
	}
	fields.collapseValues()
	return fields
}

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

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
