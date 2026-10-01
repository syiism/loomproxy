package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/lifecycle"
)

// 「标识 → 名称」命名缓存的 Redis 后端。
//
// 为什么值得外挂：正文与目录请求只带 bookId/itemId，书名/章节名全靠这张表反查。
// 纯内存时每次重启都清空——重启后到该书被重新"报出名字"之前的所有调用，
// 内容维度只能记标识、名称留空（监控页与排行榜的空值几乎都来自这里）。
// 挂到 Redis 后缓存跨重启存活，排行榜与统计的完整度直接受益。
//
// 写不阻塞请求路径：Save* 只往带缓冲通道投递，后台协程成批 pipeline 落 Redis；
// 队列满了就丢弃并计数（丢的只是"缓存条目"，内存里仍然有，下次同名再写即可）。
// 读失败一律当未命中：猜一个名字比留空更糟。

const (
	subjectKeyPrefix   = "loomproxy:subject:"
	subjectStoreTTL    = 24 * time.Hour // 与进程内 TTL 同量级：名称几乎不变，也要能跟着改名刷新
	subjectWriteQueue  = 4096
	subjectWriteBatch  = 128
	subjectFlushEvery  = 200 * time.Millisecond
	subjectReadTimeout = 300 * time.Millisecond // 读在请求路径上：宁可当没命中，也不拖慢调用
)

type subjectWrite struct {
	key   string
	value string
}

type redisSubjectStore struct {
	rdb     redis.UniversalClient
	writes  chan subjectWrite
	dropped atomic.Int64
}

func subjectBookKey(source, ident string) string {
	return subjectKeyPrefix + "book:" + source + "|" + ident
}
func subjectChapterKey(source, ident string) string {
	return subjectKeyPrefix + "chapter:" + source + "|" + ident
}

// newRedisSubjectStore 尝试接入 Redis：未启用或连不上返回 false（调用方保持纯内存行为）
func newRedisSubjectStore(ctx context.Context) (*redisSubjectStore, bool) {
	cfg := conf.Config
	if cfg == nil || !cfg.RedisEnabled {
		return nil, false
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%d", cfg.RedisHost, cfg.RedisPort),
		Password:     cfg.RedisPassword,
		DB:           cfg.RedisDB,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  subjectReadTimeout,
		WriteTimeout: 2 * time.Second,
	})
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		log.Printf("WARNING: 命名缓存未能接入 Redis（%v），保持纯内存：重启后正文调用的书名/章节名仍会为空", err)
		return nil, false
	}
	s := &redisSubjectStore{rdb: rdb, writes: make(chan subjectWrite, subjectWriteQueue)}
	go s.run(ctx)
	return s, true
}

// run 后台批量落盘：攒够一批或到点就 pipeline 写；ctx 结束前把队列里的剩余项刷完
func (s *redisSubjectStore) run(ctx context.Context) {
	ticker := time.NewTicker(subjectFlushEvery)
	defer ticker.Stop()
	batch := make([]subjectWrite, 0, subjectWriteBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		p := s.rdb.Pipeline()
		for _, w := range batch {
			p.Set(ctx, w.key, w.value, subjectStoreTTL)
		}
		if _, err := p.Exec(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("WARNING: 命名缓存写入 Redis 失败（%d 条）: %v", len(batch), err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case w := <-s.writes:
			batch = append(batch, w)
			if len(batch) >= subjectWriteBatch {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			// 关停：把已排队的写完再退出（队列里可能还有几百条）
			for {
				select {
				case w := <-s.writes:
					batch = append(batch, w)
					if len(batch) >= subjectWriteBatch {
						flush()
						continue
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (s *redisSubjectStore) enqueue(key, value string) {
	if key == "" || value == "" {
		return
	}
	select {
	case s.writes <- subjectWrite{key: key, value: value}:
	default:
		// 队列满：丢持久化，内存缓存仍在。计数只在非零时随关停日志出现
		if n := s.dropped.Add(1); n%1000 == 1 {
			log.Printf("WARNING: 命名缓存写 Redis 队列已满，累计丢弃 %d 条（内存缓存不受影响）", n)
		}
	}
}

// splitSubject 解析 name\x00media；media 可为空
func splitSubject(v string) (string, string) {
	if i := strings.IndexByte(v, 0); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func (s *redisSubjectStore) SaveBook(source, ident, name, media string) {
	if name == "" && media == "" {
		return
	}
	s.enqueue(subjectBookKey(source, ident), name+"\x00"+media)
}

func (s *redisSubjectStore) SaveChapter(source, ident, title string) {
	s.enqueue(subjectChapterKey(source, ident), title)
}

func (s *redisSubjectStore) get(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), subjectReadTimeout)
	defer cancel()
	v, err := s.rdb.Get(ctx, key).Result()
	if err != nil || v == "" {
		return "", false // 未命中/超时/Redis 故障，一律按没命中处理
	}
	return v, true
}

func (s *redisSubjectStore) LoadBook(source, ident string) (string, string, bool) {
	v, ok := s.get(subjectBookKey(source, ident))
	if !ok {
		return "", "", false
	}
	name, media := splitSubject(v)
	return name, media, name != "" || media != ""
}

func (s *redisSubjectStore) LoadChapter(source, ident string) (string, bool) {
	v, ok := s.get(subjectChapterKey(source, ident))
	if !ok {
		return "", false
	}
	return v, v != ""
}

// initSubjectStore 在启动时尝试把命名缓存挂到 Redis（未启用/连不上就维持纯内存行为）
func initSubjectStore(ctx context.Context) {
	s, ok := newRedisSubjectStore(ctx)
	if !ok {
		log.Printf("「标识→名称」命名缓存为纯内存（未启用 Redis 或连不上）：重启后清空，正文调用的名称维度会留空")
		return
	}
	base.SetSubjectStore(s)
	cfg := conf.Config
	log.Printf("「标识→名称」命名缓存已接入 Redis %s:%d/%d（TTL %v）：重启不再丢，排行榜与监控的名称完整度不受重启影响",
		cfg.RedisHost, cfg.RedisPort, cfg.RedisDB, subjectStoreTTL)
	lifecycle.RegisterCleanup(func() {
		base.SetSubjectStore(nil) // 先摘钩子，避免关停后仍有写入排队
		if n := s.dropped.Load(); n > 0 {
			log.Printf("命名缓存写 Redis 累计丢弃 %d 条（队列满；内存缓存期间仍正常）", n)
		}
		_ = s.rdb.Close()
	})
}
