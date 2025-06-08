package prompts

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/ai-microsoft/haystack/server/core/storage"
)

const (
	KeyTypePromptPath  = storage.KeyTypePromptPath
	InvalidWorkspaceId = -1
)

func ParseWorkspaceId(key string) int {
	v, err := strconv.Atoi(key)
	if err != nil {
		return InvalidWorkspaceId
	}

	return v
}

func EncodePromptPathKey(workspaceid int, promptPath string) []byte {
	return []byte(fmt.Sprintf("%c%d|%s", KeyTypePromptPath, workspaceid, promptPath))
}

func DecodePromptPathKey(key string) (int, string) {
	if !storage.IsKeyType(key, KeyTypePromptPath) {
		return InvalidWorkspaceId, ""
	}

	key = key[1:]

	parts := strings.SplitN(key, "|", 2)
	if len(parts) != 2 {
		return InvalidWorkspaceId, ""
	}

	return ParseWorkspaceId(parts[0]), parts[1]
}

func EncodeFloat32Vector(vector []float32) ([]byte, error) {
	buf := new(bytes.Buffer)
	for _, f := range vector {
		err := binary.Write(buf, binary.LittleEndian, f)
		if err != nil {
			return nil, fmt.Errorf("failed to write float32 to buffer: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func DecodeToFloat32Vector(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("invalid data length: must be a multiple of 4 for float32 vector, got %d", len(data))
	}

	count := len(data) / 4
	vector := make([]float32, count)

	reader := bytes.NewReader(data)
	for i := 0; i < count; i++ {
		err := binary.Read(reader, binary.LittleEndian, &vector[i])
		if err != nil {
			return nil, fmt.Errorf("failed to read float32 from buffer at index %d: %w", i, err)
		}
	}
	return vector, nil
}
