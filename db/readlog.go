package db

import (
	"log"
	"sync"
)

// P99 第一步的出声口：读库失败与「本来就没这条记录」必须在服务端分得开——
// 响应值与界面一律不动（拍板口径），这里只负责让失败在日志里有一句。
//
// 去重复用 P59/P36 那一族的判据：读在热路径上，每请求一条 ERROR 会把真正该看的
// 读数泡坏——同一 (落点, 表) 只喊一次，后续失败静默。key 由调用方按
// "落点:表" 给（如 "admin_stats:users"），落点是语义位置不是文件行号，
// 行号会随代码漂移，漂了就会把同一个失败喊成两种。
var readFailOnce sync.Map

func LogReadFail(key string, err error) {
	if _, ok := readFailOnce.LoadOrStore(key, struct{}{}); ok {
		return
	}
	log.Printf("ERROR: 读库失败，按空结果处理（该落点只报一次）%s: %v", key, err)
}
