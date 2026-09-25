package baidu

import (
	"encoding/json"
	"testing"
)

func TestEncodePathListEscapesSpecialCharacters(t *testing.T) {
	got, err := encodePathList(`/影视/片名\"特别集\\final.mkv`)
	if err != nil {
		t.Fatalf("encodePathList() error = %v", err)
	}

	var paths []string
	if err := json.Unmarshal([]byte(got), &paths); err != nil {
		t.Fatalf("encoded path is invalid JSON: %v", err)
	}
	if len(paths) != 1 || paths[0] != `/影视/片名\"特别集\\final.mkv` {
		t.Fatalf("decoded paths = %#v", paths)
	}
}
