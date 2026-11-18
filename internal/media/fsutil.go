package media

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// writeJSONAtomic 把 JSON 序列化后原子写入目标文件。
func writeJSONAtomic(dest string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(dest, bytes.NewReader(data))
}

// writeAtomic 把流内容写入临时文件后原子改名，避免半截图片被并发请求读到。
func writeAtomic(dest string, r io.Reader) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 改名成功后 Remove 对不存在的文件无效，可安全兜底
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}
