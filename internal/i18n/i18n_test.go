package i18n

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEnglishCatalogIsValidAndTranslatesStructuredErrors(t *testing.T) {
	decoder := json.NewDecoder(bytes.NewReader(englishJSON))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("invalid catalog object: token=%v err=%v", token, err)
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		key := token.(string)
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate catalog key %q", key)
		}
		seen[key] = struct{}{}
		var translated string
		if err := decoder.Decode(&translated); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(formatVerbs(key), formatVerbs(translated)) {
			t.Fatalf("format verbs differ for %q: %v != %v", key, formatVerbs(key), formatVerbs(translated))
		}
	}

	var catalog map[string]string
	if err := json.Unmarshal(englishJSON, &catalog); err != nil {
		t.Fatalf("invalid embedded English catalog: %v", err)
	}
	if got := T(English, "语言已保存：%s", "English"); got != "Language saved: English" {
		t.Fatalf("translation = %q", got)
	}
	cause := errors.New("connection refused")
	err := Errorf("无法连接 Mihomo API: %w", cause)
	if got := Error(English, err); got != "Unable to connect to the Mihomo API: connection refused" {
		t.Fatalf("localized error = %q", got)
	}
	if got := Error(Chinese, err); got != "无法连接 Mihomo API: connection refused" {
		t.Fatalf("Chinese error changed = %q", got)
	}
	pathErr := Errorf("%s路径必须是绝对路径", M("数据目录"))
	if got := Error(English, pathErr); got != "data directory path must be absolute" {
		t.Fatalf("localized message argument = %q", got)
	}
	if got := pathErr.Error(); got != "数据目录路径必须是绝对路径" {
		t.Fatalf("source error changed = %q", got)
	}
	if got := Error(English, errors.New("规则")); got != "规则" {
		t.Fatalf("foreign error was translated as a catalog key: %q", got)
	}
}

func formatVerbs(value string) []byte {
	verbs := make([]byte, 0)
	for index := 0; index < len(value); index++ {
		if value[index] != '%' || index+1 >= len(value) {
			continue
		}
		if value[index+1] == '%' {
			index++
			continue
		}
		for index++; index < len(value); index++ {
			if strings.ContainsRune("vTtbcdoOxXUeEfFgGspxqwc", rune(value[index])) {
				verbs = append(verbs, value[index])
				break
			}
		}
	}
	return verbs
}

func TestFromArgsUsesLastValidExplicitLanguage(t *testing.T) {
	for _, test := range []struct {
		args  []string
		want  Language
		found bool
	}{
		{args: nil, want: "", found: false},
		{args: []string{"--lang", "en"}, want: English, found: true},
		{args: []string{"status", "--lang=zh"}, want: Chinese, found: true},
		{args: []string{"--lang=en", "--lang", "zh"}, want: Chinese, found: true},
		{args: []string{"--lang", "invalid"}, want: "", found: false},
		{args: []string{"profile", "add", "--", "--lang=en"}, want: "", found: false},
	} {
		got, found := FromArgs(test.args)
		if got != test.want || found != test.found {
			t.Errorf("FromArgs(%q) = %q, %v; want %q, %v", test.args, got, found, test.want, test.found)
		}
	}
}
