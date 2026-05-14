package imagehost

import "testing"

func TestExtractJSONPath(t *testing.T) {
	got, err := extractJSONPath([]byte(`{"data":{"url":"https://example.com/a.png"}}`), "data.url")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.com/a.png" {
		t.Fatalf("unexpected url: %s", got)
	}
}
