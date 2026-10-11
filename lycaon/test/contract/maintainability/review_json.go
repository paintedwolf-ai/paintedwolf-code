package maintainability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func uniqueReviewKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var objects []map[string]bool
	var keys []bool
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				objects = append(objects, map[string]bool{})
				keys = append(keys, true)
			case '[':
				objects = append(objects, nil)
				keys = append(keys, false)
			case '}', ']':
				objects, keys = objects[:len(objects)-1], keys[:len(keys)-1]
				if len(keys) > 0 && objects[len(objects)-1] != nil {
					keys[len(keys)-1] = true
				}
			}
			continue
		}
		if len(objects) == 0 || objects[len(objects)-1] == nil {
			continue
		}
		last := len(objects) - 1
		if keys[last] {
			key, ok := token.(string)
			if !ok {
				return fmt.Errorf("review key is not a string")
			}
			if objects[last][key] {
				return fmt.Errorf("duplicate review key %q", key)
			}
			objects[last][key] = true
		}
		keys[last] = !keys[last]
	}
}
