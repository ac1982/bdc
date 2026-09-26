package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

type bigResult struct {
	Zeta  int64 `json:"zeta"`
	Alpha struct {
		Y uint64 `json:"y"`
		B string `json:"b"`
	} `json:"alpha"`
}

func TestWriteJSON(t *testing.T) {
	var r struct{ bigResult }
	r.Zeta = 9007199254740993 // 2^53 + 1: lost if it passes through float64
	r.Alpha.Y = 18446744073709551615
	r.Alpha.B = "x"
	var buf bytes.Buffer
	if err := writeJSON(&buf, "ls", jsonOnly{r}, &baidu.Error{Op: "列出目录 /x", Code: -9, Message: "不存在"}); err != nil {
		t.Fatal(err)
	}
	want := `{
  "alpha": {
    "b": "x",
    "y": 18446744073709551615
  },
  "command": "ls",
  "error": {
    "code": -9,
    "exitCode": 2,
    "kind": "input",
    "message": "列出目录 /x: 不存在 (错误码 -9)"
  },
  "ok": false,
  "zeta": 9007199254740993
}
`
	if buf.String() != want {
		t.Fatalf("got\n%s", buf.String())
	}
}

func TestWriteJSONNilResult(t *testing.T) {
	var buf bytes.Buffer
	var res *treeResult // typed nil inside the interface
	if err := writeJSON(&buf, "tree", jsonOnly{res}, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"ok": false`) {
		t.Fatal(buf.String())
	}
}

// jsonOnly adapts any value to Result for these tests.
type jsonOnly struct{ v any }

func (j jsonOnly) Human(w io.Writer)            {}
func (j jsonOnly) MarshalJSON() ([]byte, error) { return json.Marshal(j.v) }
