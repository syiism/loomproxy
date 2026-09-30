package utils

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"loomproxy-go/conf"
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
		if err == nil {
			var result interface{}
			if json.Unmarshal([]byte(val), &result) == nil {
				return result, true
			}
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
		c.redis.Set(context.Background(), key, data, c.ttl)
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
		c.redis.Del(context.Background(), key)
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
