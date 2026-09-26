package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ac1982/baidunetdisk-cli/internal/baidu"
)

// Result is what a command did. Its exported fields, through their json tags,
// are the --json document; Human writes the same thing for people.
type Result interface {
	Human(w io.Writer)
}

// writeJSON writes one document: the result's fields plus ok, command and
// error. Keys come out sorted at every level; numbers pass through as
// json.Number, so large integers such as fs ids keep every digit.
func writeJSON(w io.Writer, command string, res Result, err error) error {
	doc := map[string]any{}
	if res != nil {
		data, merr := json.Marshal(res)
		if merr != nil {
			return merr
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		if merr := dec.Decode(&doc); merr != nil {
			return fmt.Errorf("%T is not a JSON object: %w", res, merr)
		}
		if doc == nil { // a nil pointer marshals as null
			doc = map[string]any{}
		}
	}
	doc["ok"] = err == nil
	doc["command"] = command
	if err != nil {
		k := classify(err)
		e := map[string]any{"kind": k, "message": err.Error(), "exitCode": k.ExitCode()}
		if code := baidu.Code(err); code != 0 {
			e["code"] = code
		}
		doc["error"] = e
	}
	data, merr := json.MarshalIndent(doc, "", "  ")
	if merr != nil {
		return merr
	}
	_, werr := fmt.Fprintf(w, "%s\n", data)
	return werr
}
