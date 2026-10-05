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

// noHistoryError 表示该地块当前生效绑定站连一年历年资料都没有，
// 集合试走给不出范围。与“正常的空结果”区分，API 映射为 422。
type noHistoryError struct {
	plot    string
	station string
}

func (e noHistoryError) Error() string {
	return fmt.Sprintf("地块 %s 当前绑定站 %s 没有任何历年气温资料，给不出试走范围",
		e.plot, e.station)
}

// AsNoHistory 判断错误是否为“无历年资料”。
func AsNoHistory(err error) bool {
	_, ok := err.(noHistoryError)
	return ok
}
