package utils

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"loomproxy/conf"
)

type cacheEntry struct {
	key     string
	value   interface{}
	expires time.Time
}

type Cache struct {
	mu      sync.Mutex
	items   map[string]*list.Element
	lruList *list.List
	maxSize int
	ttl     time.Duration

	redis    redis.UniversalClient
	redisDB  int
	useRedis bool

	// 接口缓存的 Redis 那一段坏掉时，原来**一个字都不说**：`Set` 连返回的错误都没接，
	// `Get` 把「连不上」和「没命中」回成同一个 (nil,false)（判据同 P56 那条「返回值两种含义」）。
	// 症状是"缓存好像没在工作"却查不出为什么——所以这里只加两件东西：计数 + 状态翻转时出声。
	rSet, rGet, rDel redisState
}

// redisState 一段 Redis 调用的健康状态：累计失败次数，且**只在坏↔好翻转时出声**。
// 判据取自 P36/P48 那一条：热路径上每次失败都喊，会把真正该看的读数泡坏。
type redisState struct {
	fails atomic.Int64
	bad   atomic.Bool
}

func (s *redisState) fail(kind string, err error) {
	n := s.fails.Add(1)
	if s.bad.CompareAndSwap(false, true) {
		log.Printf("ERROR: 接口缓存的 Redis %s失败（累计第 %d 次，此后同一种只计数不出声）：%v", kind, n, err)
	}
}

func (s *redisState) ok(kind string) {
	if s.bad.CompareAndSwap(true, false) {
		log.Printf("接口缓存的 Redis %s已恢复（此前累计失败 %d 次）", kind, s.fails.Load())
	}
}

func NewCache(maxSize int, ttl time.Duration) *Cache {
	return &Cache{
		items:   make(map[string]*list.Element),
		lruList: list.New(),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

func NewCacheWithRedis(maxSize int, ttl time.Duration) (*Cache, error) {
	c := &Cache{
		items:   make(map[string]*list.Element),
		lruList: list.New(),
		maxSize: maxSize,
		ttl:     ttl,
	}

	if !conf.Config.RedisEnabled {
		return c, nil
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%d", conf.Config.RedisHost, conf.Config.RedisPort),
		Password:     conf.Config.RedisPassword,
		DB:           conf.Config.RedisDB,
		ReadTimeout:  time.Duration(conf.Config.CacheTTL) * time.Second,
		WriteTimeout: 3 * time.Second,
		DialTimeout:  3 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return c, fmt.Errorf("redis ping failed: %w (falling back to in-memory cache)", err)
	}

	c.redis = rdb
	c.redisDB = conf.Config.RedisDB
	c.useRedis = true
	return c, nil
}

func (c *Cache) Get(key string) (interface{}, bool) {
	if c.useRedis {
		val, err := c.redis.Get(context.Background(), key).Result()
		if err != nil {
			// redis.Nil 是**正常未命中**，不许多出声；其余（连不上、超时、被拒）才是故障
			if !errors.Is(err, redis.Nil) {
				c.rGet.fail("读取", err)
			}
			return nil, false
		}
		c.rGet.ok("读取")
		var result interface{}
		if json.Unmarshal([]byte(val), &result) == nil {
			return result, true
		}
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}

	entry := elem.Value.(*cacheEntry)
	if time.Now().After(entry.expires) {
		c.lruList.Remove(elem)
		delete(c.items, key)
		return nil, false
	}

	c.lruList.MoveToFront(elem)
	return entry.value, true
}

func (c *Cache) Set(key string, value interface{}) {
	if c.useRedis {
		data, _ := json.Marshal(value)
		if err := c.redis.Set(context.Background(), key, data, c.ttl).Err(); err != nil {
			c.rSet.fail("写入", err)
			return
		}
		c.rSet.ok("写入")
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		entry := elem.Value.(*cacheEntry)
		entry.value = value
		entry.expires = time.Now().Add(c.ttl)
		c.lruList.MoveToFront(elem)
		return
	}

	entry := &cacheEntry{
		key:     key,
		value:   value,
		expires: time.Now().Add(c.ttl),
	}
	elem := c.lruList.PushFront(entry)
	c.items[key] = elem

	if c.lruList.Len() > c.maxSize {
		oldest := c.lruList.Back()
		if oldest != nil {
			c.lruList.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
}

func (c *Cache) Del(key string) {
	if c.useRedis {
		// 这一处比 Set 失败更要紧：Del 是**失效**通路。悄悄失败等于"撤销返回成功、缓存照样命中"
		// （`utils/apikey.go` 的注释里警告过的就是这种形状），所以它单独计一份状态。
		if err := c.redis.Del(context.Background(), key).Err(); err != nil {
			c.rDel.fail("失效", err)
			return
		}
		c.rDel.ok("失效")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		c.lruList.Remove(elem)
		delete(c.items, key)
	}
}

// DelPrefix 删除所有以 prefix 开头的缓存键（如 "datasource:"）。
// 用于管理端写操作后的批量失效——此类缓存键由 CacheKey 散列生成、无法逐个枚举，
// 故键格式须为「可识别前缀 + ":" + 散列」。Redis 模式走 SCAN 匹配，内存模式全量遍历，
// 均为低频管理操作，代价可接受
func (c *Cache) DelPrefix(prefix string) {
	if c.useRedis {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		iter := c.redis.Scan(ctx, 0, prefix+"*", 100).Iterator()
		for iter.Next(ctx) {
			c.redis.Del(ctx, iter.Val())
		}
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, elem := range c.items {
		if strings.HasPrefix(key, prefix) {
			c.lruList.Remove(elem)
			delete(c.items, key)
		}
	}
}

func (c *Cache) Close() {
	if c.useRedis && c.redis != nil {
		c.redis.Close()
	}
}

// Clear 清空全部缓存项（当前仅清理内存部分；主要用于测试隔离）
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element)
	c.lruList.Init()
}

func (c *Cache) Key(prefix string, params map[string]interface{}) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		v, _ := json.Marshal(params[k])
		h.Write(v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func CacheKey(prefix string, params map[string]interface{}) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		v, _ := json.Marshal(params[k])
		h.Write(v)
	}
	return hex.EncodeToString(h.Sum(nil))
}

var defaultCache *Cache
var defaultCacheOnce sync.Once

func DefaultCache() *Cache {
	defaultCacheOnce.Do(func() {
		var err error
		defaultCache, err = NewCacheWithRedis(
			conf.Config.CacheMaxSize,
			time.Duration(conf.Config.CacheTTL)*time.Second,
		)
		if err != nil {
			// Redis 不可用时打印警告，使用纯内存缓存
			defaultCache = NewCache(
				conf.Config.CacheMaxSize,
				time.Duration(conf.Config.CacheTTL)*time.Second,
			)
		}
	})
	return defaultCache
}

// CloseAll 关闭全局缓存（优雅停机时用）
func CloseAll() {
	if defaultCache != nil {
		defaultCache.Close()
	}
}
