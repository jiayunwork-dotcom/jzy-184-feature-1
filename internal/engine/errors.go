package engine

import "fmt"

// notFoundError 表示档案不存在，API 层映射为 404。
type notFoundError struct {
	what string
	code string
}

func (e notFoundError) Error() string {
	return fmt.Sprintf("%s %s 不存在", e.what, e.code)
}

// AsNotFound 判断错误是否为“不存在”。
func AsNotFound(err error) bool {
	_, ok := err.(notFoundError)
	return ok
}
