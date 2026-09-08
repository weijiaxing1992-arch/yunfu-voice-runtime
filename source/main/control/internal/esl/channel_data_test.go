package esl

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestChannelDataSerializationBounds 保证最坏UTF8/JSON转义仍受限；不生成正文或额外事件帧。
func TestChannelDataSerializationBounds(t *testing.T) {
	headers := map[string]string{"Event-Name": "CHANNEL_DATA", "variable_text": "中文\r\n<&%20+"}
	for _, format := range []string{"txt", "plain", "json", "xml"} {
		body, err := SerializeChannelData(headers, format)
		if err != nil || len(body) == 0 || strings.Contains(body, "text/event-") {
			t.Fatal(format, err, body)
		}
		if format == "json" {
			var value map[string]string
			if json.Unmarshal([]byte(body), &value) != nil || value["variable_text"] != headers["variable_text"] {
				t.Fatal("json lost raw text")
			}
		}
	}
	if _, err := SerializeChannelData(headers, "invalid"); err == nil {
		t.Fatal("unsupported format accepted")
	}
	for index := 0; index < 160; index++ {
		headers[fmt.Sprint(index)] = "value"
	}
	if _, err := SerializeChannelData(headers, "json"); err == nil {
		t.Fatal("header budget ignored")
	}
	for _, bad := range []map[string]string{{"bad\nname": "value"}, {"large": strings.Repeat("x", 80*1024+1)}} {
		if _, err := SerializeChannelData(bad, "json"); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}
