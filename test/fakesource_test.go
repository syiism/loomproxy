package test

// 假数据源夹具本体在 testkit/fakesource（跨进程用例经 cmd/fakegateway 起同一套声明）。
// 这里只做导入期登记与常量别名，供本包用例引用。

import (
	"loomproxy-go/testkit/fakesource"
)

const (
	fakeA           = fakesource.A
	fakeB           = fakesource.B
	fakeC           = fakesource.C
	fakeLegacyGroup = fakesource.LegacyGroup
)

func init() { fakesource.Register() }
