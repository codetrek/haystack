package utils

import (
	"crypto/md5"
	"fmt"
)

func Md5Hash(data []byte) string {
	return fmt.Sprintf("%x", md5.Sum(data))
}

func Md5HashString(data string) string {
	return Md5Hash([]byte(data))
}
